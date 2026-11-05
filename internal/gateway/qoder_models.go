package gateway

// Qoder's model catalog.
//
// A signed-in account is served the models Qoder's own app lists for it
// (/algo/api/v2/model/list), not a hard-coded tier table: which models an
// account may use, what they cost in credits, which reasoning efforts they
// offer and which context windows they take are all per-account and change
// whenever Qoder re-shelves them. The listing is also the only place the
// per-model configuration a request has to carry comes from, so it is kept
// verbatim on the credential and read back when a request is shaped.
//
// Before an account is signed in — or when the listing cannot be asked — a
// static table is installed instead, so the console still shows what the site
// is currently known to serve.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
)

// qoderEffortOrder ranks Qoder's effort names, lowest first.
var qoderEffortOrder = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

// qoderStaticModel is one entry of the fallback catalog.
type qoderStaticModel struct {
	Key     string
	Name    string
	Context int
	Efforts []string
}

// qoderGlobalStaticModels is qoder.com's catalog as Qoder's app listed it on
// 2026-10-12 (its Fast tier included), with the names Qoder shows beside the
// keys. A real account's own list replaces it as soon as it signs in.
var qoderGlobalStaticModels = []qoderStaticModel{
	{Key: "ultimate", Name: "Ultimate", Context: 1_000_000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{Key: "fast", Name: "Fast", Context: 1_000_000},
	{Key: "performance", Name: "Performance", Context: 1_000_000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{Key: "efficient", Name: "Efficient", Context: 200_000},
	{Key: "smodel", Name: "Sonus", Context: 180_000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{Key: "cmodel", Name: "Cantus", Context: 180_000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{Key: "qmodel_38max", Name: "Qwen3.8-Max", Context: 180_000, Efforts: []string{"low", "medium", "xhigh"}},
	{Key: "qfmodel", Name: "Qwen3.8-Flash", Context: 180_000, Efforts: []string{"low", "medium", "xhigh"}},
	{Key: "qmodel_latest", Name: "Qwen3.7-Max", Context: 1_000_000},
	{Key: "qmodel", Name: "Qwen3.7-Plus", Context: 1_000_000},
	{Key: "kmodel_latest", Name: "Kimi-K3", Context: 180_000, Efforts: []string{"low", "high", "max"}},
	{Key: "kmodel", Name: "Kimi-K2.8-Preview", Efforts: []string{"low", "high", "max"}},
	{Key: "gmodel", Name: "GLM-5.3", Context: 180_000, Efforts: []string{"low", "high", "max"}},
	{Key: "gfmodel", Name: "GLM-5.3-Flash", Context: 1_000_000, Efforts: []string{"high", "max"}},
	{Key: "dmodel", Name: "DeepSeek-V4-Pro", Context: 1_000_000, Efforts: []string{"high", "max"}},
	{Key: "dfmodel", Name: "DeepSeek-Flash", Context: 1_000_000, Efforts: []string{"low", "high", "max"}},
	{Key: "mmodel", Name: "MiniMax-M3", Context: 180_000},
}

// qoderCNStaticModels is what Qoder CN's CLI names: its three paid tiers, plus
// the free Lite. Qoder CN's account list takes over once signed in.
var qoderCNStaticModels = []qoderStaticModel{
	{Key: "ultimate", Name: "Ultimate", Context: 1_000_000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{Key: "performance", Name: "Performance", Context: 1_000_000, Efforts: []string{"low", "medium", "high", "xhigh", "max"}},
	{Key: "efficient", Name: "Efficient", Context: 200_000},
	{Key: "lite", Name: "Lite"},
}

// qoderModelInfo is one model an account may be served, with the pieces a
// request needs: the upstream's own configuration block, its effort levels and
// its context windows.
type qoderModelInfo struct {
	Key            string
	Source         string
	DisplayName    string
	MaxInputTokens int
	// Windows are the context windows the model offers, smallest first, and
	// DefaultWindow the one Qoder's client picks unless asked for another. A
	// request that fits the default keeps it — a larger window can cost more.
	Windows       []int
	DefaultWindow int
	Efforts       []string
	DefaultEffort string
	Thinks        bool
	AlwaysThinks  bool
	Images        bool
	// Free is whether the plan pays nothing for this model, Rate the credits
	// multiple it does cost, and RateWas the price struck through beside it
	// while a discount runs (0: none).
	Free    bool
	Rate    float64
	RateWas float64
	Config  json.RawMessage
}

// routable is whether the entry names one model a client can pick. "auto" and
// "default" route between models inside Qoder and are not a model.
func (m qoderModelInfo) routable() bool {
	switch strings.TrimSpace(m.Key) {
	case "", "auto", "default":
		return false
	}
	return true
}

// largestContext is the model's biggest offered window, else what a request
// naming none gets (max_input_tokens).
func (m qoderModelInfo) largestContext() int {
	if len(m.Windows) > 0 {
		return m.Windows[len(m.Windows)-1]
	}
	return m.MaxInputTokens
}

// qoderListing is the model-list response. Qoder lists the same models under
// more than one scene, and each scene's array carries the entry verbatim.
type qoderListing struct {
	App  []json.RawMessage `json:"app"`
	Chat []json.RawMessage `json:"chat"`
}

// qoderListingModels is the models an account can pick: the "app" scene's, which
// is what Qoder's app shows and what a request names (its session_type is
// "app"), then any model only the "chat" scene still lists, inserted after the
// model it followed there — so a model picked while it was listed stays served
// after the app re-shelves. Each key once; the app's entry wins.
func qoderListingModels(listing qoderListing) []qoderModelInfo {
	merged := append([]json.RawMessage(nil), listing.App...)
	keyOf := func(raw json.RawMessage) string {
		var entry struct {
			Key string `json:"key"`
		}
		if json.Unmarshal(raw, &entry) != nil {
			return ""
		}
		return strings.TrimSpace(entry.Key)
	}
	after := -1
	for _, raw := range listing.Chat {
		index := -1
		for position, existing := range merged {
			if keyOf(existing) == keyOf(raw) {
				index = position
				break
			}
		}
		if index >= 0 {
			after = index
			continue
		}
		// Insert where the chat scene says it belongs, not at the end: a model
		// the app dropped keeps its old neighbours.
		after++
		if after >= len(merged) {
			merged = append(merged, raw)
			continue
		}
		merged = append(merged[:after], append([]json.RawMessage{raw}, merged[after:]...)...)
	}
	out := make([]qoderModelInfo, 0, len(merged))
	for _, raw := range merged {
		info, ok := qoderReadListingModel(raw)
		if !ok {
			continue
		}
		out = append(out, info)
	}
	return out
}

// qoderReadListingModel reads one listing entry, reporting whether it is a
// model an account may be served.
func qoderReadListingModel(raw json.RawMessage) (qoderModelInfo, bool) {
	var entry struct {
		Key            string          `json:"key"`
		Source         string          `json:"source"`
		Enable         bool            `json:"enable"`
		DisplayName    string          `json:"display_name"`
		IsVL           bool            `json:"is_vl"`
		IsReasoning    bool            `json:"is_reasoning"`
		MaxInputTokens int             `json:"max_input_tokens"`
		ReasoningEff   []string        `json:"reasoning_efforts"`
		ThinkingConfig json.RawMessage `json:"thinking_config"`
		ContextConfig  json.RawMessage `json:"context_config"`
		// Raw is the entry as it arrived: the price fields are read off it with
		// the promotion rules below, which need more shape than this struct keeps.
		Raw json.RawMessage
	}
	if json.Unmarshal(raw, &entry) != nil || !entry.Enable {
		return qoderModelInfo{}, false
	}
	entry.Raw = raw
	info := qoderModelInfo{
		Key:            strings.TrimSpace(entry.Key),
		Source:         entry.Source,
		DisplayName:    firstNonEmpty(strings.TrimSpace(entry.DisplayName), strings.TrimSpace(entry.Key)),
		MaxInputTokens: entry.MaxInputTokens,
		Images:         entry.IsVL,
		Thinks:         entry.IsReasoning,
		Efforts:        append([]string(nil), entry.ReasoningEff...),
		Config:         raw,
	}
	if !info.routable() {
		return qoderModelInfo{}, false
	}
	info.Windows, info.DefaultWindow = qoderWindowsOf(entry.ContextConfig)
	info.Free, info.Rate, info.RateWas = qoderPriceOf(entry.Raw)
	// thinking_config is where Qoder keeps the levels a model offers, its
	// default, and whether thinking can be turned off at all; is_reasoning
	// alone stands for a model listed without one.
	if len(entry.ThinkingConfig) > 0 {
		var thinking struct {
			Disabled json.RawMessage `json:"disabled"`
			Enabled  *struct {
				Efforts map[string]struct {
					IsDefault bool `json:"is_default"`
				} `json:"efforts"`
			} `json:"enabled"`
		}
		if json.Unmarshal(entry.ThinkingConfig, &thinking) == nil {
			info.Thinks = thinking.Enabled != nil
			info.AlwaysThinks = info.Thinks && len(thinking.Disabled) == 0
			info.Efforts = nil
			if info.Thinks {
				for name, effort := range thinking.Enabled.Efforts {
					info.Efforts = append(info.Efforts, name)
					if effort.IsDefault {
						info.DefaultEffort = name
					}
				}
				rank := func(name string) int {
					for index, known := range qoderEffortOrder {
						if known == name {
							return index
						}
					}
					return len(qoderEffortOrder)
				}
				sort.Slice(info.Efforts, func(i, j int) bool {
					if rank(info.Efforts[i]) != rank(info.Efforts[j]) {
						return rank(info.Efforts[i]) < rank(info.Efforts[j])
					}
					return info.Efforts[i] < info.Efforts[j]
				})
			}
		}
	}
	return info, true
}

// qoderPriceOf reads what a listing entry costs the plan, as Qoder's own client
// shows it beside the model: the credits multiple a request costs (0.5×), and
// the price struck through next to it while a discount runs.
//
// The rule is not "is_free". Qoder's listing marks Qwen3.8-Max is_free=true
// while pricing it at 0.5× (an off-peak discount), and its client shows the
// price, not the flag — so a model counts as free only when the listed price is
// 0 and that 0 is not a promotion. An active promotion carrying a price before
// it is a discount; a limited-time free model (Qwen3.8-Flash: 0× with its
// original 0.1× struck through) is free while it runs. is_free is consulted
// only for a listing that names no price at all.
func qoderPriceOf(raw json.RawMessage) (free bool, rate float64, rateWas float64) {
	var entry struct {
		IsFree       *bool    `json:"is_free"`
		IsFreeC      *bool    `json:"isFree"`
		PriceFactor  *float64 `json:"price_factor"`
		PriceFactorC *float64 `json:"priceFactor"`
		Original     float64  `json:"original_price_factor"`
		OriginalC    float64  `json:"originalPriceFactor"`
		// Qoder spells the promotion block both ways across its replies, and
		// once misspelled it; both are read.
		Promotion  *qoderPromotion `json:"promotion"`
		PromotionT *qoderPromotion `json:"prommotion"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		return false, 0, 0
	}
	price := entry.PriceFactor
	if price == nil {
		price = entry.PriceFactorC
	}
	if price == nil {
		isFree := entry.IsFree
		if isFree == nil {
			isFree = entry.IsFreeC
		}
		return isFree != nil && *isFree, 0, 0
	}
	promotion := entry.Promotion
	if promotion == nil {
		promotion = entry.PromotionT
	}
	before, discount := 0.0, 0.0
	if promotion != nil {
		if promotion.Before != nil {
			before = *promotion.Before
		} else if promotion.BeforeC != nil {
			before = *promotion.BeforeC
		}
		discount = qoderMaxFloat(promotion.Discount, promotion.DiscountC)
	}
	discounted := promotion != nil && promotion.Active && before > 0
	rate = qoderMaxFloat(*price, 0)
	rateWas = qoderMaxFloat(entry.Original, entry.OriginalC)
	if discounted {
		rateWas = before
		if rate == 0 {
			// A promotion that zeroes the price for a while still costs the
			// discounted multiple, not nothing.
			rate = before * discount
		}
	}
	if rateWas <= rate {
		rateWas = 0
	}
	return *price == 0 && !discounted, rate, rateWas
}

type qoderPromotion struct {
	Active    bool     `json:"active"`
	Before    *float64 `json:"before_promotion_price_factor"`
	BeforeC   *float64 `json:"beforePromotionPriceFactor"`
	Discount  float64  `json:"discount_factor"`
	DiscountC float64  `json:"discountFactor"`
}

func qoderMaxFloat(values ...float64) float64 {
	best := 0.0
	for index, value := range values {
		if index == 0 || value > best {
			best = value
		}
	}
	return best
}

// qoderWindowsOf reads a model's context windows from context_config — the
// entries behind Qoder's Context setting (200K / 400K / 1M) — smallest first,
// with the default. max_input_tokens is only what a request naming no window
// gets, so it is not a window.
func qoderWindowsOf(raw json.RawMessage) (windows []int, defaultWindow int) {
	if len(raw) == 0 {
		return nil, 0
	}
	var config map[string]struct {
		TokenCount int `json:"token_count"`
		IsDefault  any `json:"is_default"`
	}
	if json.Unmarshal(raw, &config) != nil {
		return nil, 0
	}
	for _, entry := range config {
		if entry.TokenCount <= 0 {
			continue
		}
		found := false
		for _, known := range windows {
			if known == entry.TokenCount {
				found = true
				break
			}
		}
		if !found {
			windows = append(windows, entry.TokenCount)
		}
		switch value := entry.IsDefault.(type) {
		case bool:
			if value {
				defaultWindow = entry.TokenCount
			}
		case string:
			if value == "true" || value == "1" {
				defaultWindow = entry.TokenCount
			}
		case float64:
			if value == 1 {
				defaultWindow = entry.TokenCount
			}
		}
	}
	sort.Ints(windows)
	return windows, defaultWindow
}

// qoderStaticModelsFor is the site's fallback catalog.
func qoderStaticModelsFor(site qoderSite) []qoderStaticModel {
	if site.AuthType == domain.AuthTypeQoderCN {
		return qoderCNStaticModels
	}
	return qoderGlobalStaticModels
}

// qoderCatalogModels turns a catalog into the provider's model rows. IDs are
// Qoder's own model keys, because that is what a request has to name.
func qoderCatalogModels(providerID string, models []qoderStaticModel, infos []qoderModelInfo) []domain.Model {
	out := make([]domain.Model, 0, len(models)+len(infos))
	if len(infos) > 0 {
		for _, info := range infos {
			model := domain.Model{
				ID:            info.Key,
				ProviderID:    providerID,
				Name:          info.DisplayName,
				Protocol:      domain.ProtocolOpenAIChat,
				ContextLength: info.largestContext(),
				Free:          info.Free,
				Rate:          info.Rate,
				RateWas:       info.RateWas,
				InMenu:        true,
			}
			fillModelTokenBudgets(&model)
			out = append(out, model)
		}
		return out
	}
	for _, entry := range models {
		model := domain.Model{
			ID:            entry.Key,
			ProviderID:    providerID,
			Name:          entry.Name,
			Protocol:      domain.ProtocolOpenAIChat,
			ContextLength: entry.Context,
			InMenu:        true,
		}
		fillModelTokenBudgets(&model)
		out = append(out, model)
	}
	return out
}

// qoderFetchListing asks the site's model endpoint. The call carries the same
// COSY envelope every /algo call does, and the listing is a GET that still
// sends a body: the codec's form of "{}", which is what the signature covers.
//
// Any host may answer it: a listing is a cheap read, so a refused or unreachable
// gateway is walked past rather than reported — the next candidate lists the
// same models.
func qoderFetchListing(ctx context.Context, site qoderSite, account qoderAccount) (json.RawMessage, error) {
	wire := qoderEncodeBody([]byte("{}"))
	var lastErr error
	for _, host := range site.qoderHosts() {
		listing, err := qoderFetchListingFrom(ctx, host+qoderModelsPath, wire, account)
		if err == nil {
			return listing, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("qoder: 该站点没有可用的模型列表主机")
	}
	return nil, lastErr
}

// qoderFetchListingFrom is one attempt at one host.
func qoderFetchListingFrom(ctx context.Context, endpoint, wire string, account qoderAccount) (json.RawMessage, error) {
	headers, err := qoderCOSYHeaders(endpoint, account, wire, 0)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, strings.NewReader(wire))
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode/100 != 2 {
		return nil, &qoderError{Message: qoderErrorMessage(body, response.StatusCode), Status: response.StatusCode}
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("Qoder 返回了无法解析的模型列表")
	}
	return json.RawMessage(body), nil
}

// syncQoderProviderModels installs the account's own model list when it is
// signed in, and the site's static catalog when it is not. The listing that
// worked is kept on the credential, because a request carries each model's
// configuration from it.
func (s *Server) syncQoderProviderModels(providerID string) (domain.Provider, error) {
	provider, err := s.router.ProviderByID(providerID)
	if err != nil {
		return domain.Provider{}, err
	}
	site := qoderSiteOf(provider)
	var listing json.RawMessage
	var models []domain.Model
	if provider.QoderPAT != nil && !provider.QoderPAT.Disconnected && provider.QoderPAT.AccessToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		listing, err = qoderFetchListing(ctx, site, qoderAccountOf(provider))
		cancel()
		if err == nil {
			credential := *provider.QoderPAT
			credential.Models = listing
			if stored, storeErr := s.router.SetProviderQoderPAT(providerID, credential); storeErr == nil {
				provider = stored
			}
			var parsed qoderListing
			if json.Unmarshal(listing, &parsed) == nil {
				models = qoderCatalogModels(providerID, nil, qoderListingModels(parsed))
			}
		} else {
			s.logs.AddApp("warn", "qoder model listing failed: "+err.Error(), providerID)
		}
	}
	if len(models) == 0 {
		// Either not signed in yet, or Qoder would not answer: the catalog the
		// site is known by keeps the console usable.
		models = qoderCatalogModels(providerID, qoderStaticModelsFor(site), nil)
	}
	updated, err := s.router.UpdateProviderModels(providerID, models, "healthy")
	if err != nil {
		return provider, err
	}
	if err := s.saveState(); err != nil {
		return updated, err
	}
	return updated, nil
}

// qoderAccountOf is the signed-in identity a COSY envelope is signed for.
func qoderAccountOf(provider domain.Provider) qoderAccount {
	if provider.QoderPAT == nil {
		return qoderAccount{}
	}
	return qoderAccount{
		UID:          provider.QoderPAT.UID,
		Name:         provider.QoderPAT.Name,
		Email:        provider.QoderPAT.Email,
		Token:        provider.QoderPAT.AccessToken,
		MachineID:    provider.QoderPAT.MachineID,
		RefreshToken: provider.QoderPAT.RefreshToken,
	}
}

// qoderModelFor is the listing entry behind a model name, and whether the
// account may be served it. A listing that was never read answers nothing, and
// the caller falls back to naming the model to Qoder without a configuration.
func qoderModelFor(provider domain.Provider, model string) (qoderModelInfo, bool) {
	if provider.QoderPAT == nil || len(provider.QoderPAT.Models) == 0 {
		return qoderModelInfo{}, false
	}
	var listing qoderListing
	if json.Unmarshal(provider.QoderPAT.Models, &listing) != nil {
		return qoderModelInfo{}, false
	}
	wanted := strings.TrimSpace(model)
	for _, info := range qoderListingModels(listing) {
		if strings.EqualFold(info.Key, wanted) {
			return info, true
		}
	}
	return qoderModelInfo{}, false
}
