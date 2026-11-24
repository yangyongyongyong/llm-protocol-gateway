package gateway

import (
	"fmt"
	"strings"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// normalizeCustomProvider 统一模板字段默认值。UpstreamBaseURL 永远不放在模板
// 上：同一类型可能被多套不同环境复用，具体地址只能由 Provider 实例提供。
func normalizeCustomProvider(provider domain.CustomProvider) domain.CustomProvider {
	provider.ID = strings.TrimSpace(provider.ID)
	provider.Name = strings.TrimSpace(provider.Name)
	provider.Description = strings.TrimSpace(provider.Description)
	provider.AuthHeader = strings.TrimSpace(provider.AuthHeader)
	provider.DefaultModel = strings.TrimSpace(provider.DefaultModel)
	if provider.AuthHeader == "" {
		provider.AuthHeader = "Authorization"
	}
	if provider.Protocol == "" {
		provider.Protocol = domain.ProtocolOpenAIChat
	}
	provider.Script.UpstreamBaseURL = ""
	provider.Script.Language = strings.TrimSpace(provider.Script.Language)
	if provider.Script.Language == "" {
		provider.Script.Language = "python"
	}
	provider.Script.Entrypoint = strings.TrimSpace(provider.Script.Entrypoint)
	if provider.Script.Entrypoint == "" {
		provider.Script.Entrypoint = "adapter.py"
	}
	provider.Script.HealthPath = strings.TrimSpace(provider.Script.HealthPath)
	if provider.Script.HealthPath == "" {
		provider.Script.HealthPath = "/health"
	}
	return provider
}

func cloneCustomProvider(provider domain.CustomProvider) domain.CustomProvider {
	return provider
}

func isBuiltinCustomProviderID(id string) bool {
	for _, builtin := range builtinCustomProviders() {
		if builtin.ID == strings.TrimSpace(id) {
			return true
		}
	}
	return false
}

func ensureNotBuiltin(provider domain.CustomProvider) error {
	if provider.Builtin || isBuiltinCustomProviderID(provider.ID) {
		return fmt.Errorf("custom provider %q is built in and cannot be modified", provider.ID)
	}
	return nil
}

// CustomProviders 返回模板快照，调用方不能直接修改 Router 内部状态。
func (r *Router) CustomProviders() []domain.CustomProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.CustomProvider, len(r.state.CustomProviders))
	copy(out, r.state.CustomProviders)
	return out
}

func (r *Router) CustomProviderByID(id string) (domain.CustomProvider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, provider := range r.state.CustomProviders {
		if provider.ID == strings.TrimSpace(id) {
			return cloneCustomProvider(provider), nil
		}
	}
	return domain.CustomProvider{}, fmt.Errorf("custom provider %q not found", id)
}

func (r *Router) AddCustomProvider(provider domain.CustomProvider) (domain.CustomProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider = normalizeCustomProvider(provider)
	if provider.Name == "" {
		return domain.CustomProvider{}, fmt.Errorf("custom provider name is required")
	}
	if err := ensureNotBuiltin(provider); err != nil {
		return domain.CustomProvider{}, err
	}
	for _, item := range r.state.CustomProviders {
		if strings.EqualFold(item.Name, provider.Name) {
			return domain.CustomProvider{}, fmt.Errorf("custom provider name %q already exists", provider.Name)
		}
	}
	if provider.ID == "" {
		provider.ID = uniqueID(slug(provider.Name), func(id string) bool {
			for _, item := range r.state.CustomProviders {
				if item.ID == id {
					return true
				}
			}
			return false
		})
	} else {
		for _, item := range r.state.CustomProviders {
			if item.ID == provider.ID {
				return domain.CustomProvider{}, fmt.Errorf("custom provider id %q already exists", provider.ID)
			}
		}
	}
	now := nowRFC3339()
	provider.CreatedAt = now
	provider.UpdatedAt = now
	r.state.CustomProviders = append(r.state.CustomProviders, provider)
	return provider, nil
}

// UpdateCustomProvider 替换模板定义。调用方负责在更新后把模板同步到所有实例；
// Router 不在这里自动重启进程，避免把网络/子进程操作放进持锁路径。
func (r *Router) UpdateCustomProvider(id string, incoming domain.CustomProvider) (domain.CustomProvider, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id = strings.TrimSpace(id)
	incoming = normalizeCustomProvider(incoming)
	if incoming.Name == "" {
		return domain.CustomProvider{}, fmt.Errorf("custom provider name is required")
	}
	for index := range r.state.CustomProviders {
		if r.state.CustomProviders[index].ID != id {
			continue
		}
		for otherIndex := range r.state.CustomProviders {
			if otherIndex != index && strings.EqualFold(r.state.CustomProviders[otherIndex].Name, incoming.Name) {
				return domain.CustomProvider{}, fmt.Errorf("custom provider name %q already exists", incoming.Name)
			}
		}
		if err := ensureNotBuiltin(r.state.CustomProviders[index]); err != nil {
			return domain.CustomProvider{}, err
		}
		updated := incoming
		updated.ID = id
		updated.Builtin = r.state.CustomProviders[index].Builtin
		updated.CreatedAt = r.state.CustomProviders[index].CreatedAt
		updated.UpdatedAt = nowRFC3339()
		r.state.CustomProviders[index] = updated
		return updated, nil
	}
	return domain.CustomProvider{}, fmt.Errorf("custom provider %q not found", id)
}

func (r *Router) DeleteCustomProvider(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	id = strings.TrimSpace(id)
	for _, provider := range r.state.Providers {
		if provider.CustomProviderID == id {
			return fmt.Errorf("custom provider %q is still used by provider %q", id, provider.Name)
		}
	}
	for index := range r.state.CustomProviders {
		if r.state.CustomProviders[index].ID == id {
			if err := ensureNotBuiltin(r.state.CustomProviders[index]); err != nil {
				return err
			}
			r.state.CustomProviders = append(r.state.CustomProviders[:index], r.state.CustomProviders[index+1:]...)
			return nil
		}
	}
	return fmt.Errorf("custom provider %q not found", id)
}

// ProvidersUsingCustomProvider 返回引用该模板的实例快照，供类型更新后同步。
func (r *Router) ProvidersUsingCustomProvider(id string) []domain.Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]domain.Provider, 0)
	for _, provider := range r.state.Providers {
		if !provider.Deleted && provider.CustomProviderID == strings.TrimSpace(id) {
			out = append(out, provider)
		}
	}
	return out
}
