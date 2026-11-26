package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/luca/llm-protocol-gateway/internal/domain"
	"github.com/luca/llm-protocol-gateway/internal/monitor"
	"github.com/luca/llm-protocol-gateway/internal/store"
)

// The gateway's own cost ledger.
//
// Prices here are what the gateway charges itself for a request's tokens, taken
// from the vendor's published rate card and entered by the operator. They are
// not the vendor's invoice, and they are deliberately not fetched from a third
// party catalogue: most of what this gateway fronts (coding plans, subscription
// logins, domestic relays) is not in any public per-token table, and a guessed
// price is worse than an absent one — an absent price is reported as 未计价,
// while a wrong one is silently summed into a total someone then acts on.
//
// Cost is computed when the request finishes and stored per request and per day
// aggregate, so editing a price later never rewrites what a past request cost.

// PriceStore is the persistence the ledger needs; *store.Store satisfies it.
type PriceStore interface {
	LoadModelPrices() ([]domain.ModelPrice, error)
	SaveModelPrice(domain.ModelPrice) error
	DeleteModelPrice(providerID, modelID string) error
}

// CostBackfillStore 是 PriceStore 的可选扩展能力，用于在单一事务内补算历史未计价请求并同步更新日桶。
type CostBackfillStore interface {
	BackfillUnpricedCosts(from, to time.Time, resolve func(store.UnpricedRequestRow) store.PricedRequestResolution) (store.CostBackfillResult, error)
}

// priceTable is the lookup cache. One map read per request, never a query: the
// alternative is a SQLite read on the hot path for a number that changes a few
// times a year.
type priceTable struct {
	mu     sync.RWMutex
	rows   map[string]domain.ModelPrice
	loaded bool
	// generation counts invalidations. A load reads it before querying the
	// store and only stores its result if nothing invalidated in between: the
	// rows it read are then still the current ones. Without it, an admin's save
	// that landed while a cold load was in flight was undone by that load —
	// stale rates for the life of the process, silently.
	generation uint64
	// loading is closed when the load now in flight finishes, so calls that
	// arrive meanwhile wait for it instead of each querying the store.
	loading chan struct{}
}

func priceKey(providerID, modelID string) string {
	return strings.TrimSpace(providerID) + "\x00" + strings.TrimSpace(modelID)
}

func (s *Server) SetPriceStore(store PriceStore) {
	if s == nil {
		return
	}
	s.priceStore = store
	s.prices = &priceTable{rows: map[string]domain.ModelPrice{}}
}

// priceRows returns the cached table, loading it once on first use. A store that
// cannot be read is reported through the API rather than silently pricing
// everything at zero.
func (s *Server) priceRows() (map[string]domain.ModelPrice, error) {
	if s == nil || s.prices == nil {
		return nil, errors.New("price table not configured")
	}
	for {
		s.prices.mu.RLock()
		loaded, rows, inFlight := s.prices.loaded, s.prices.rows, s.prices.loading
		s.prices.mu.RUnlock()
		if loaded {
			return rows, nil
		}
		if s.priceStore == nil {
			return rows, nil
		}
		if inFlight != nil {
			// Another request is loading the table: its result is ours too
			// (or it was invalidated again, and the loop loads once more).
			<-inFlight
			continue
		}
		s.prices.mu.Lock()
		if s.prices.loaded {
			rows := s.prices.rows
			s.prices.mu.Unlock()
			return rows, nil
		}
		if s.prices.loading != nil {
			inFlight := s.prices.loading
			s.prices.mu.Unlock()
			<-inFlight
			continue
		}
		generation := s.prices.generation
		done := make(chan struct{})
		s.prices.loading = done
		s.prices.mu.Unlock()

		stored, err := s.priceStore.LoadModelPrices()
		indexed := make(map[string]domain.ModelPrice, len(stored))
		for _, price := range stored {
			indexed[priceKey(price.ProviderID, price.ModelID)] = price
		}

		s.prices.mu.Lock()
		s.prices.loading = nil
		// Only a load nothing invalidated may be cached: otherwise the rows it
		// read may predate the write that invalidated the table, and storing them
		// would undo that write for the life of the process.
		current := err == nil && s.prices.generation == generation
		if current {
			s.prices.rows = indexed
			s.prices.loaded = true
		}
		s.prices.mu.Unlock()
		close(done)
		if err != nil {
			return nil, err
		}
		// A load that raced a write still answers this caller with what the store
		// held a moment ago — a faithful answer to its question — but the next
		// caller loads again.
		return indexed, nil
	}
}

// lookupPrice finds the row that prices one model, in this order:
//
//  1. this provider's exact model row
//  2. this provider's "*" default row
//  3. the global exact model row
//  4. the global "*" default row
//
// A provider row is a deliberate override of the shared rate card, so it wins
// even when it is only a catch-all. Official Claude accounts, relays and other
// providers of the same model otherwise share one global row instead of each
// needing its own copy.
func (s *Server) lookupPrice(providerID, model string) (domain.ModelPrice, bool) {
	rows, err := s.priceRows()
	if err != nil || len(rows) == 0 {
		return domain.ModelPrice{}, false
	}
	model = upstreamModelForPricing(model)
	for _, scope := range []string{providerID, globalPriceProvider} {
		if scope == "" {
			continue
		}
		if model != "" {
			if price, ok := rows[priceKey(scope, model)]; ok {
				return price, true
			}
		}
		if price, ok := rows[priceKey(scope, storePriceWildcard)]; ok {
			return price, true
		}
	}
	return domain.ModelPrice{}, false
}

// storePriceWildcard mirrors store.PriceWildcard without importing store here.
const storePriceWildcard = "*"

// globalPriceProvider is the provider id that means "every provider without a
// row of its own". The price table's scope picker writes rows under it.
const globalPriceProvider = "*"

// costOf prices one request's usage. It returns priced=false when no row covers
// the model, so callers can tell "free" from "we do not know".
//
// The subtraction is the whole point: InputTokens already contains the cache
// reads and the cache writes (see TokenUsage), so pricing the full input at the
// uncached rate and then adding the cache tiers on top would bill those tokens
// twice.
func (s *Server) costOf(providerID, model string, usage TokenUsage) (int64, bool) {
	price, ok := s.lookupPrice(providerID, model)
	if !ok {
		return 0, false
	}
	cached := usage.CacheTokens
	if cached < 0 {
		cached = 0
	}
	if cached > usage.InputTokens {
		cached = usage.InputTokens
	}
	writes := usage.CacheWriteTokens
	if writes < 0 {
		writes = 0
	}
	if writes > usage.InputTokens-cached {
		writes = usage.InputTokens - cached
	}
	plain := usage.InputTokens - cached - writes
	// Rates are per million tokens, so the product is a fraction of a dollar;
	// rounding to whole micros keeps a day's sum equal to the sum of its
	// requests instead of drifting with each display rounding.
	costUSD := (float64(plain)*price.InputUSDPerMTok +
		float64(usage.OutputTokens)*price.OutputUSDPerMTok +
		float64(cached)*price.CacheReadUSDPerMTok +
		float64(writes)*price.CacheWriteUSDPerMTok) / 1e6
	cost := int64(math.Round(costUSD * 1e6))
	if cost < 0 {
		cost = 0
	}
	return cost, true
}

// upstreamModelForPricing mirrors the first step of resolveRequestLogModelForUsage:
// the logged model is the upstream id, or the historical "alias -> upstream"
// pair whose right side is what the vendor actually billed.
func upstreamModelForPricing(loggedModel string) string {
	raw := strings.TrimSpace(loggedModel)
	if _, right, ok := strings.Cut(raw, "->"); ok {
		if real := strings.TrimSpace(right); real != "" {
			return real
		}
	}
	return raw
}

// allRatesZero reports a row that prices every token tier at zero. -0 counts as
// zero; a negative rate is not zero and is rejected by the range check first.
func priceRowIsZero(p domain.ModelPrice) bool {
	for _, rate := range []float64{p.InputUSDPerMTok, p.OutputUSDPerMTok, p.CacheReadUSDPerMTok, p.CacheWriteUSDPerMTok} {
		if rate != 0 {
			return false
		}
	}
	return true
}

// ── console API ──────────────────────────────────────────────────────────────

func (s *Server) handleListPrices(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	rows, err := s.priceRows()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to read price table: "+err.Error())
		return
	}
	prices := make([]domain.ModelPrice, 0, len(rows))
	for _, price := range rows {
		prices = append(prices, price)
	}
	sort.Slice(prices, func(i, j int) bool {
		if prices[i].ProviderID != prices[j].ProviderID {
			return prices[i].ProviderID < prices[j].ProviderID
		}
		return prices[i].ModelID < prices[j].ModelID
	})
	writeJSON(w, http.StatusOK, map[string]any{"prices": prices})
}

func (s *Server) handleSavePrice(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.priceStore == nil {
		writeOpenAIError(w, http.StatusConflict, "price table requires the SQLite store")
		return
	}
	var price domain.ModelPrice
	if err := json.NewDecoder(r.Body).Decode(&price); err != nil {
		writeOpenAIError(w, http.StatusBadRequest, "invalid price json: "+err.Error())
		return
	}
	price.ProviderID = strings.TrimSpace(price.ProviderID)
	price.ModelID = strings.TrimSpace(price.ModelID)
	if price.ProviderID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "price needs providerId")
		return
	}
	if price.ModelID == "" {
		price.ModelID = storePriceWildcard
	}
	if price.ProviderID != globalPriceProvider {
		if _, err := s.router.ProviderByID(price.ProviderID); err != nil {
			writeOpenAIError(w, http.StatusNotFound, "provider "+price.ProviderID+" not found")
			return
		}
	}
	for _, rate := range []float64{price.InputUSDPerMTok, price.OutputUSDPerMTok, price.CacheReadUSDPerMTok, price.CacheWriteUSDPerMTok} {
		if rate < 0 {
			writeOpenAIError(w, http.StatusBadRequest, "rates can not be negative; omit the row to leave a model unpriced")
			return
		}
	}
	// An all-zero global row is refused. It is the last row lookupPrice tries
	// before giving up, so it would turn every model without a price of its own
	// into "priced at $0" and erase the 未计价 signal the ledger relies on — a
	// mistyped save would silently zero the whole cost total. A global row is
	// still allowed when it carries real rates. A provider's own exact row or its
	// "*" row may be all zero on purpose: it is scoped to one provider, and one of
	// the four rates may legitimately be 0 (a model with no cache write fee).
	// A global exact row is scoped to one model but still overrides nothing less
	// specific, so it follows the same rule as the global catch-all.
	if price.ProviderID == globalPriceProvider && priceRowIsZero(price) {
		writeOpenAIError(w, http.StatusBadRequest,
			"a global price row needs at least one non-zero rate; an all-zero global row would mark every unpriced model as priced at $0. Omit the row to leave models unpriced")
		return
	}
	if err := s.priceStore.SaveModelPrice(price); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to save price: "+err.Error())
		return
	}
	s.invalidatePrices()
	writeJSON(w, http.StatusOK, price)
}

func (s *Server) handleDeletePrice(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.priceStore == nil {
		writeOpenAIError(w, http.StatusConflict, "price table requires the SQLite store")
		return
	}
	providerID := strings.TrimSpace(r.PathValue("providerID"))
	modelID := strings.TrimSpace(r.PathValue("modelID"))
	if providerID == "" {
		writeOpenAIError(w, http.StatusBadRequest, "missing provider id")
		return
	}
	if modelID == "" {
		modelID = storePriceWildcard
	}
	if err := s.priceStore.DeleteModelPrice(providerID, modelID); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to delete price: "+err.Error())
		return
	}
	s.invalidatePrices()
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

// invalidatePrices drops the cache after a write. The next request reloads the
// whole table, which is fine at this size and avoids keeping two copies in sync.
func (s *Server) invalidatePrices() {
	if s.prices == nil {
		return
	}
	s.prices.mu.Lock()
	s.prices.rows = nil
	s.prices.loaded = false
	s.prices.generation++
	s.prices.mu.Unlock()
}

// costBackfillRequest 是 POST /__prices/backfill 的可选时间范围参数；缺省时按网关本地时区的「今天」补算。
type costBackfillRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// parseCostBackfillRange 解析补算请求的时间范围，语义与 handleRequestStats 的 from/to 保持一致。
func parseCostBackfillRange(fromRaw, toRaw string, now time.Time) (from, to, toInclusive time.Time, err error) {
	localNow := now.Local()
	loc := localNow.Location()
	dayStart := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc)
	from = dayStart
	toInclusive = dayStart
	to = dayStart.AddDate(0, 0, 1)

	if raw := strings.TrimSpace(fromRaw); raw != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
			from = parsed
		} else if parsed, parseErr := time.ParseInLocation("2006-01-02", raw, loc); parseErr == nil {
			from = parsed
		} else {
			return time.Time{}, time.Time{}, time.Time{}, errors.New("invalid from date; expected YYYY-MM-DD or RFC3339")
		}
	}
	if raw := strings.TrimSpace(toRaw); raw != "" {
		if parsed, parseErr := time.Parse(time.RFC3339, raw); parseErr == nil {
			to = parsed
			toInclusive = parsed
			if strings.Contains(raw, "23:59:59") {
				day := time.Date(parsed.Year(), parsed.Month(), parsed.Day(), 0, 0, 0, 0, parsed.Location())
				toInclusive = day
				to = day.AddDate(0, 0, 1)
			}
		} else if parsed, parseErr := time.ParseInLocation("2006-01-02", raw, loc); parseErr == nil {
			toInclusive = parsed
			to = parsed.AddDate(0, 0, 1)
		} else {
			return time.Time{}, time.Time{}, time.Time{}, errors.New("invalid to date; expected YYYY-MM-DD or RFC3339")
		}
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, time.Time{}, errors.New("to must be after from")
	}
	return from, to, toInclusive, nil
}

// handleBackfillPrices 按当前价格表补算指定时间区间内 priced=0 的历史请求，
// 并实时刷新日聚合内存桶与统计接口缓存；已计价的历史请求绝不会被重写。
func (s *Server) handleBackfillPrices(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	if s.priceStore == nil {
		writeOpenAIError(w, http.StatusConflict, "price table requires the SQLite store")
		return
	}
	backfillStore, ok := s.priceStore.(CostBackfillStore)
	if !ok {
		writeOpenAIError(w, http.StatusConflict, "price backfill is not supported by current store")
		return
	}

	var reqBody costBackfillRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil && !errors.Is(err, io.EOF) {
			writeOpenAIError(w, http.StatusBadRequest, "invalid backfill json: "+err.Error())
			return
		}
	}

	from, to, toInclusive, err := parseCostBackfillRange(reqBody.From, reqBody.To, time.Now())
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 先把内存中攒批中的请求日志与异步日桶队列刷盘，避免刚完成但尚未落盘的请求漏算或重复计价。
	s.FlushRequestLogs()
	if err := s.logs.FlushUsage(r.Context()); err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to flush usage queue: "+err.Error())
		return
	}

	state := s.router.State()
	keysByID := make(map[string]domain.APIKey, len(state.APIKeys))
	keysByName := make(map[string]domain.APIKey, len(state.APIKeys))
	for _, key := range state.APIKeys {
		keysByID[key.ID] = key
		if name := strings.TrimSpace(key.Name); name != "" {
			keysByName[strings.ToLower(name)] = key
		}
	}

	result, err := backfillStore.BackfillUnpricedCosts(from, to, func(row store.UnpricedRequestRow) store.PricedRequestResolution {
		logItem := monitor.RequestLog{
			Time:             row.Time,
			APIKeyID:         row.APIKeyID,
			APIKeyName:       row.APIKeyName,
			RouteID:          row.RouteID,
			ProviderID:       row.ProviderID,
			Model:            row.Model,
			ProtocolFlow:     row.ProtocolFlow,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheTokens:      row.CacheTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			PeerGatewayID:    row.PeerGatewayID,
			PeerDirection:    row.PeerDirection,
		}
		resolvedModel := resolveRequestLogModelForUsage(s.router, logItem, keysByID, keysByName)
		costMicros, priced := s.costOf(row.ProviderID, resolvedModel, TokenUsage{
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheTokens:      row.CacheTokens,
			CacheWriteTokens: row.CacheWriteTokens,
		})
		if !priced {
			return store.PricedRequestResolution{Priced: false}
		}

		keyID := strings.TrimSpace(row.APIKeyID)
		if keyID == "" {
			keyID = "_anonymous"
		}
		userID := "_anonymous"
		if key, matched := lookupLogAPIKey(logItem, keysByID, keysByName); matched {
			uid := ownerUserIDForStats(key.OwnerUserID)
			if uid != "" {
				userID = uid
			}
		}
		providerID := strings.TrimSpace(row.ProviderID)
		if providerID == "" {
			providerID = "_unknown"
		}
		modelBucket := ""
		if m, ok := monitor.NormalizeModelForStats(resolvedModel); ok {
			modelBucket = m
		}
		outputProtocol := strings.TrimSpace(monitor.OutputProtocolFromFlow(row.ProtocolFlow))
		peerGatewayID := ""
		if strings.EqualFold(strings.TrimSpace(row.PeerDirection), "ingress") {
			peerGatewayID = strings.TrimSpace(row.PeerGatewayID)
		}

		return store.PricedRequestResolution{
			Day:            row.Time.Local().Format("2006-01-02"),
			KeyID:          keyID,
			UserID:         userID,
			ProviderID:     providerID,
			ModelBucket:    modelBucket,
			OutputProtocol: outputProtocol,
			PeerGatewayID:  peerGatewayID,
			CostMicros:     costMicros,
			Priced:         true,
		}
	})
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "failed to backfill unpriced costs: "+err.Error())
		return
	}

	if result.Updated > 0 {
		s.RebuildUsageStats()
		s.requestStatsCache.clear()
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"updated":    result.Updated,
		"costMicros": result.CostMicros,
		"from":       from.Local().Format("2006-01-02"),
		"to":         toInclusive.Local().Format("2006-01-02"),
	})
}
