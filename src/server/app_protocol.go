package server

import "strings"

// ValidAppDomain 判断输入是否为应用的完整域名或短名。
func ValidAppDomain(value string) bool {
	value = strings.ToLower(value)
	return !isIPHost(value) && (validLabel.MatchString(value) || dnsName(value))
}

// ValidAPIPath 判断输入是否为斜杠开头的单段小写路径。
func ValidAPIPath(value string) bool {
	alias, ok := strings.CutPrefix(value, "/")
	return ok && validAlias.MatchString(alias)
}

// ValidManualToken 判断输入是否符合 Bearer 令牌格式。
func ValidManualToken(value string) bool {
	return validManualToken.MatchString(value)
}
