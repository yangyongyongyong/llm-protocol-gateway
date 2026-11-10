// Package launchagent 读我们自己的 LaunchAgent plist。
//
// 之所以要读它，是因为一个只会在出事时暴露的问题：plist 装好之后，它指向的
// 启动脚本（源码目录里的 scripts/run-gateway-service.sh）随目录一起被删掉了，
// 而 plist 还留在 ~/Library/LaunchAgents 里。KeepAlive=true 会让 launchd 不停
// 重拉一个必然失败的任务，stderr 日志无声地涨（实测一台机器上 51MB，全是
// 「No such file or directory」），而且它还占着 18093 不放——桌面 App 想自己
// 托管同一端口时会一直失败。
//
// 解析放在这里而不是 desktop/ 下面，是为了能在 Linux 的 CI 上直接测：桌面壳
// 是独立模块，那边的测试不参与主仓库的 go test ./...。
package launchagent

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Plist 是 plist 里我们读得懂的字段，其余（EnvironmentVariables、ProcessType
// 之类）不关心，解析时整段跳过。
type Plist struct {
	// Label 是 launchd 里的作业名，例如 com.luca.llm-protocol-gateway。
	Label string
	// ProgramArguments 是启动命令，第一个元素通常是指令解释器。
	ProgramArguments []string
	// StandardOutPath / StandardErrorPath 是这个作业把输出写去哪。
	StandardOutPath   string
	StandardErrorPath string
}

// Parse 读 plist XML 的顶层字段。
//
// 没用 plutil：那东西只有 macOS 有，测试跑不了；而我们自己的 plist 是
// install-launchagent.sh 用模版 sed 出来的，结构固定，顶层的 string 与
// string 数组足够覆盖。
func Parse(data []byte) (Plist, error) {
	var out Plist
	decoder := xml.NewDecoder(bytes.NewReader(data))
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return Plist{}, fmt.Errorf("解析 plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil {
			return Plist{}, fmt.Errorf("解析 plist 的 key: %w", err)
		}
		valueStart, err := nextStartElement(decoder)
		if err != nil {
			// key 后面没有值：文件不完整。已经读到的字段仍然可用。
			return out, nil
		}
		switch key {
		case "Label":
			err = decodeString(decoder, valueStart, &out.Label)
		case "ProgramArguments":
			out.ProgramArguments, err = decodeStringArray(decoder, valueStart)
		case "StandardOutPath":
			err = decodeString(decoder, valueStart, &out.StandardOutPath)
		case "StandardErrorPath":
			err = decodeString(decoder, valueStart, &out.StandardErrorPath)
		default:
			err = decoder.Skip()
		}
		if err != nil {
			return out, err
		}
	}
}

// MissingProgram 返回 ProgramArguments 里第一个「指着一个不存在的绝对路径」
// 的参数；都还在则返回空串。
//
// 只认绝对路径：/bin/bash 这类解释器一般都在，真正会消失的是源码目录里的
// 脚本，而两者都是绝对路径，所以用 exists 一个个问过去即可。相对路径不参与
// 判断——它相对于 WorkingDirectory，猜错了会误删一个其实正常的 plist。
func (p Plist) MissingProgram(exists func(string) bool) string {
	for _, arg := range p.ProgramArguments {
		arg = strings.TrimSpace(arg)
		if arg == "" || !strings.HasPrefix(arg, "/") {
			continue
		}
		if !exists(arg) {
			return arg
		}
	}
	return ""
}

// nextStartElement 跳过空白与注释，返回下一个开始标签。
func nextStartElement(decoder *xml.Decoder) (xml.StartElement, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return xml.StartElement{}, err
		}
		switch typed := token.(type) {
		case xml.StartElement:
			return typed, nil
		case xml.EndElement:
			return xml.StartElement{}, io.EOF
		}
	}
}

// decodeString 读一个 <string>；类型对不上就整段跳过，不报错——plist 的某个
// 键换了类型不该让整个清理流程停摆。
func decodeString(decoder *xml.Decoder, start xml.StartElement, out *string) error {
	if start.Name.Local != "string" {
		return decoder.Skip()
	}
	return decoder.DecodeElement(out, &start)
}

// decodeStringArray 读 <array> 里的 <string> 元素。
func decodeStringArray(decoder *xml.Decoder, start xml.StartElement) ([]string, error) {
	if start.Name.Local != "array" {
		return nil, decoder.Skip()
	}
	var out []string
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("解析 plist 数组: %w", err)
		}
		switch typed := token.(type) {
		case xml.StartElement:
			if typed.Name.Local != "string" {
				if err := decoder.Skip(); err != nil {
					return nil, err
				}
				continue
			}
			var value string
			if err := decoder.DecodeElement(&value, &typed); err != nil {
				return nil, fmt.Errorf("解析 plist 数组元素: %w", err)
			}
			out = append(out, value)
		case xml.EndElement:
			if typed.Name.Local == "array" {
				return out, nil
			}
		}
	}
}
