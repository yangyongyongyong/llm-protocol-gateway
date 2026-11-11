package gateway

import (
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptHash 生成密码摘要。
//
// 集中在这里而不是散在调用点，是因为 cost 必须统一：哪天要把 DefaultCost
// 调高，改一处就全部生效，不会留下几个仍在用旧 cost 的入口。
func bcryptHash(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(strings.TrimSpace(password)), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// bcryptPasswordMatches 比对密码与已存摘要。
//
// 空摘要一律返回 false，不吞掉 bcrypt 的错误：peer 账号正常情况下有摘要
// （注册时写入），为空说明这行是别的途径造出来的，此时"密码匹配"应当是否，
// 而不是让一个畸形 hash 变成后门。
func bcryptPasswordMatches(hash, password string) bool {
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(strings.TrimSpace(password))) == nil
}
