// qoderprobe: 用网关内部的 COSY 签名逻辑探测 Qoder /algo 接口（本地分析用）。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/gateway"
)

func main() {
	token := flag.String("token", "", "dt- access token")
	uid := flag.String("uid", "", "account uid")
	machineID := flag.String("machine", "", "machine id")
	path := flag.String("path", "/algo/api/v2/activity", "algo path")
	method := flag.String("method", "GET", "GET|POST")
	body := flag.String("body", "", "POST body (raw json)")
	flag.Parse()

	if *token == "" || *uid == "" || *machineID == "" {
		fmt.Println("需要 -token -uid -machine")
		os.Exit(2)
	}

	rawURL := "https://api3.qoder.sh" + *path
	wire := ""
	if strings.EqualFold(*method, "POST") {
		wire = *body
		if wire == "" {
			wire = "{}"
		}
	}

	headers, err := gateway.QoderCOSYHeadersForProbe(rawURL, *uid, *token, *machineID, wire, time.Now().Unix())
	if err != nil {
		fmt.Println("签名失败:", err)
		os.Exit(1)
	}

	var rd io.Reader
	if strings.EqualFold(*method, "POST") {
		rd = strings.NewReader(wire)
	}
	req, _ := http.NewRequest(strings.ToUpper(*method), rawURL, rd)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	fmt.Printf("%s %s\n", strings.ToUpper(*method), rawURL)
	if wire != "" {
		fmt.Printf("body: %s\n", wire)
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("签名头: %s\n\n", strings.Join(keys, ", "))

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Println("请求失败:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	fmt.Printf("状态: %d\n", resp.StatusCode)
	var pretty interface{}
	if json.Unmarshal(raw, &pretty) == nil {
		b, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Println(string(b))
	} else {
		fmt.Println(string(raw))
	}
}
