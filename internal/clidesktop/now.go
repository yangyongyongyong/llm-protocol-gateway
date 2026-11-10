package clidesktop

import "time"

// nowStamp 是备份文件的时间戳，测试可替换。
var nowStamp = func() string { return time.Now().Format("20060102-150405") }
