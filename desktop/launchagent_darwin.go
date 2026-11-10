//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/launchagent"
)

// staleAgentMaxLogBytes 是一个 LaunchAgent 日志文件的上限。超过它的日志在
// 清理残留服务时一并截断：出问题的那个 plist 会让 launchd 每 10 秒重拉一次
// 必然失败的脚本，实测一台机器上 stderr 涨到 51MB。
const staleAgentMaxLogBytes = 1 << 20 // 1MiB

// staleLaunchAgent 描述一个「装了但跑不起来」的残留 LaunchAgent。
type staleLaunchAgent struct {
	Label   string
	Plist   string
	Missing string
	Logs    []string
}

// inspectStaleLaunchAgent 读 plist，判断它指向的启动脚本是否还在。
//
// 只在「文件确实存在、且 ProgramArguments 里有绝对路径指向不存在的文件」时
// 返回结果。正常安装的 LaunchAgent（脚本还在）一律返回 nil，不会被误删。
func inspectStaleLaunchAgent(label string) *staleLaunchAgent {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
	raw, err := os.ReadFile(plistPath)
	if err != nil {
		return nil
	}
	plist, err := launchagent.Parse(raw)
	if err != nil {
		return nil
	}
	missing := plist.MissingProgram(func(path string) bool {
		_, statErr := os.Stat(path)
		return statErr == nil
	})
	if missing == "" {
		return nil
	}
	stale := &staleLaunchAgent{
		Label:   label,
		Plist:   plistPath,
		Missing: missing,
	}
	// 日志路径同样可能已经不存在；只留下真正是文件的那些。
	for _, path := range []string{plist.StandardErrorPath, plist.StandardOutPath} {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if info, statErr := os.Stat(path); statErr == nil && !info.IsDir() {
			stale.Logs = append(stale.Logs, path)
		}
	}
	return stale
}

// removeStaleLaunchAgent 停掉残留作业、删掉 plist，并回收它撑大的日志。
//
// 顺序很重要：先 bootout 再删 plist。反过来的话，launchd 在自己下次重拉之前
// 仍会拿着内存里的作业定义跑一次，那一次又会在刚删掉的位置上重新写日志。
//
// 返回给用户看的说明；没有残留时返回空串。
func removeStaleLaunchAgent(label string) (string, error) {
	stale := inspectStaleLaunchAgent(label)
	if stale == nil {
		return "", nil
	}
	uid := ""
	if u, err := user.Current(); err == nil {
		uid = u.Uid
	}
	if uid == "" {
		uid = fmt.Sprintf("%d", os.Getuid())
	}
	// bootout 失败不算错：作业很可能本来就没在跑（脚本早就没了，launchd 反复
	// 失败到放弃）。真正要保证的是 plist 被删掉，否则下次开机照样重来。
	target := fmt.Sprintf("gui/%s/%s", uid, label)
	if out, err := exec.Command("launchctl", "bootout", target).CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if !strings.Contains(msg, "No such process") && !strings.Contains(msg, "Could not find service") && !strings.Contains(msg, "not found") {
			return "", fmt.Errorf("停止残留 LaunchAgent %s 失败：%s (%v)", label, msg, err)
		}
	}
	var errs []error
	if err := os.Remove(stale.Plist); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("删除残留 plist %s 失败: %w", stale.Plist, err))
	}
	// 日志截断到上限而不是整份删掉：里面可能还有用户想留的线索，但 51MB 的
	// 重复行既占地方又让人以为服务还在跑。
	for _, path := range stale.Logs {
		if info, statErr := os.Stat(path); statErr == nil && info.Size() > staleAgentMaxLogBytes {
			if truncErr := os.Truncate(path, 0); truncErr != nil {
				errs = append(errs, fmt.Errorf("清空日志 %s 失败: %w", path, truncErr))
			}
		}
	}
	msg := fmt.Sprintf("已清理残留 LaunchAgent：%s（它指向的 %s 已不存在，launchd 会不断重拉一个必然失败的作业）", label, stale.Missing)
	if len(stale.Logs) > 0 {
		msg += "\n日志：" + strings.Join(stale.Logs, "、")
	}
	return msg, errors.Join(errs...)
}

func stopLaunchAgent(label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", fmt.Errorf("empty launch agent label")
	}
	// 先处理「装了但脚本已删」的残留：这种 plist 的 KeepAlive 会无限重拉，
	// 单纯 bootout 只是把当前那个失败进程停掉，10 秒后 launchd 又拉起一个，
	// 继续占着端口、继续写日志。必须连 plist 一起删。
	cleanupMsg, cleanupErr := removeStaleLaunchAgent(label)

	uid := ""
	if u, err := user.Current(); err == nil {
		uid = u.Uid
	}
	if uid == "" {
		uid = fmt.Sprintf("%d", os.Getuid())
	}
	target := fmt.Sprintf("gui/%s/%s", uid, label)

	// Prefer bootout; fall back to unload for older launchd.
	out, err := exec.Command("launchctl", "bootout", target).CombinedOutput()
	if err == nil {
		if cleanupMsg != "" {
			return cleanupMsg, cleanupErr
		}
		return fmt.Sprintf("已停止 LaunchAgent：%s", label), nil
	}
	msg := strings.TrimSpace(string(out))
	if strings.Contains(msg, "No such process") || strings.Contains(msg, "Could not find service") || strings.Contains(msg, "not found") {
		// 清理成功时这个结果是预期的（作业本来就没在跑），把清理说明给出去
		// 比「未在运行」更有用。
		if cleanupMsg != "" {
			return cleanupMsg, cleanupErr
		}
		return fmt.Sprintf("LaunchAgent %s 未在运行（或已停止）", label), nil
	}

	home, _ := os.UserHomeDir()
	plist := fmt.Sprintf("%s/Library/LaunchAgents/%s.plist", home, label)
	if _, statErr := os.Stat(plist); statErr == nil {
		out2, err2 := exec.Command("launchctl", "unload", plist).CombinedOutput()
		if err2 == nil {
			if cleanupMsg != "" {
				return cleanupMsg, cleanupErr
			}
			return fmt.Sprintf("已 unload LaunchAgent：%s", plist), nil
		}
		return "", fmt.Errorf("bootout failed: %s; unload failed: %s (%v)", msg, strings.TrimSpace(string(out2)), err2)
	}
	return "", fmt.Errorf("停止 LaunchAgent 失败：%s (%v)", msg, err)
}
