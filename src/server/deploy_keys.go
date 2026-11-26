package server

import (
	"crypto/sha256"
	"log"
	"net/http"
	"time"
)

const (
	deployKeyPrefix = "app-plug-deploy-"
	deployKeyTTL    = 15 * time.Minute
	maxDeployKeys   = 64
)

// 写锁保护签发、兑换及路由保存。密钥只存摘要，重启即失效。
type deployKey struct {
	issuer    string
	issuerSum [sha256.Size]byte
	input     appInput
	expiresAt time.Time
}

type deployKeyReceipt struct {
	Token     string    `json:"token"`
	Domain    string    `json:"domain"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type deployKeyClaim struct {
	Domain string `json:"domain"`
}

func (s *routeStore) issueDeployKey(c *catalog, actor, issuerToken string, input appInput) (int, any) {
	if c.policy.APIMode == apiOff {
		return http.StatusConflict, apiProblem{"部署 API 已停用，请先在访问设置中启用"}
	}
	next, err := c.policy.resolveApp(input, actor)
	if err != nil {
		return http.StatusBadRequest, apiProblem{err.Error()}
	}
	if c.byHost[next.host] != nil {
		return http.StatusConflict, apiProblem{"域名已被使用，临时密钥不能覆盖已有应用"}
	}
	if len(c.apps) >= maxApps {
		return http.StatusConflict, apiProblem{"路由数量已达上限"}
	}
	now := time.Now()
	s.pruneDeployKeys(c.policy, now)
	if len(s.deployKeys) >= maxDeployKeys {
		return http.StatusConflict, apiProblem{"待使用的临时密钥已达上限，请使用已有密钥或等待它们过期"}
	}
	token, _, err := newAdminToken()
	if err != nil {
		log.Printf("生成部署密钥失败：%v", err)
		return http.StatusInternalServerError, apiProblem{"无法生成临时部署密钥"}
	}
	token = deployKeyPrefix + token
	input.Domain, input.Address = next.host, next.Address
	key := deployKey{issuer: actor, issuerSum: sha256.Sum256([]byte(issuerToken)), input: input, expiresAt: now.Add(deployKeyTTL)}
	if s.deployKeys == nil {
		s.deployKeys = make(map[[sha256.Size]byte]deployKey)
	}
	s.deployKeys[sha256.Sum256([]byte(token))] = key
	return http.StatusCreated, deployKeyReceipt{Token: token, Domain: next.host, ExpiresAt: key.expiresAt}
}

func (s *routeStore) pruneDeployKeys(p *policy, now time.Time) {
	for sum, key := range s.deployKeys {
		if !key.valid(p, now) {
			delete(s.deployKeys, sum)
		}
	}
}

func (k deployKey) valid(p *policy, now time.Time) bool {
	return now.Before(k.expiresAt) && p.byToken[k.issuerSum] == k.issuer && p.Principals[k.issuer].Admin
}

func (s *routeStore) redeemDeployKey(r *http.Request, host, apiPath string, claim deployKeyClaim) (int, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Context().Err() != nil {
		return http.StatusRequestTimeout, apiProblem{"请求已超时或取消"}
	}
	c := s.current.Load()
	if c.policy.APIPath != apiPath {
		return http.StatusNotFound, apiProblem{"接口不存在"}
	}
	if status, problem := c.policy.checkAPIHost(host); status != 0 {
		return status, problem
	}
	sum := sha256.Sum256([]byte(bearerToken(r)))
	key, ok := s.deployKeys[sum]
	if !ok || !key.valid(c.policy, time.Now()) {
		delete(s.deployKeys, sum)
		return http.StatusUnauthorized, apiProblem{"临时部署密钥无效、已使用或已过期"}
	}
	domain, err := c.policy.appDomain(claim.Domain)
	if err != nil || domain != key.input.Domain {
		return http.StatusForbidden, apiProblem{"临时部署密钥不适用于该域名"}
	}
	next, err := c.policy.resolveApp(key.input, key.issuer)
	if err != nil {
		log.Printf("临时部署密钥兑换失败，域名 %q：%v", domain, err)
		return http.StatusBadRequest, apiProblem{"预设部署配置已失效，请联系签发人"}
	}
	// 串行检查冲突、保存并消费；保存失败不消耗密钥。
	status, result := s.persistRoute(c, key.issuer, http.MethodPost, nil, next)
	if status == http.StatusCreated {
		delete(s.deployKeys, sum)
		// 临时密钥调用者不需要知道管理员填写的运行参数。
		return status, struct {
			Domain string `json:"domain"`
			URL    string `json:"url"`
		}{next.host, "https://" + next.host + "/"}
	}
	return status, result
}
