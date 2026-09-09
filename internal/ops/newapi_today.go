package ops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

// Read the upstream account's own consumption records, never local gateway totals.
// Pin the query end and reject changed totals/duplicate rows during pagination.
func (p *Prober) newAPIAccountToday(ctx context.Context, upstream core.Upstream, credential string, headers map[string]string, now time.Time) *core.UpstreamToday {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	timezone := "UTC+08:00"
	local := now.In(time.FixedZone(timezone, 8*3600))
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()).Unix()
	end := now.Unix()
	query := url.Values{"type": {"2"}, "start_timestamp": {strconv.FormatInt(start, 10)}, "end_timestamp": {strconv.FormatInt(end, 10)}}
	out := &core.UpstreamToday{Timezone: &timezone}
	body, _, err := p.getWithQuery(ctx, upstream, "/api/log/self/stat", credential, headers, query)
	if err == nil {
		var stat struct {
			Success bool `json:"success"`
			Data    *struct {
				Quota *float64 `json:"quota"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &stat) == nil && stat.Success && stat.Data != nil && stat.Data.Quota != nil && *stat.Data.Quota >= 0 {
			cost := *stat.Data.Quota / newAPIQuotaPerUSD
			out.Cost = &cost
		}
	}
	type entry struct {
		ID      int64  `json:"id"`
		Created int64  `json:"created_at"`
		Type    int    `json:"type"`
		Input   *int64 `json:"prompt_tokens"`
		Output  *int64 `json:"completion_tokens"`
	}
	seen := map[string]int{}
	var count int64
	var total, input, output int64
	inputComplete, outputComplete := true, true
	query.Set("page_size", "100")
	for page := 1; page <= 20; page++ {
		query.Set("p", strconv.Itoa(page))
		// AgentRouter redirects the trailing-slash route. Keep automatic
		// redirects disabled so account credentials cannot leave the upstream.
		var status int
		body, status, err = p.getWithQuery(ctx, upstream, "/api/log/self", credential, headers, query)
		if status == http.StatusMovedPermanently || status == http.StatusTemporaryRedirect || status == http.StatusPermanentRedirect || status == http.StatusNotFound {
			// Some NewAPI versions register only the trailing-slash route.
			// Probe that fixed route on the configured host; ignore Location.
			body, _, err = p.getWithQuery(ctx, upstream, "/api/log/self/", credential, headers, query)
		}
		if err != nil {
			break
		}
		var response struct {
			Success bool `json:"success"`
			Data    *struct {
				Total *int64  `json:"total"`
				Items []entry `json:"items"`
			} `json:"data"`
		}
		if json.Unmarshal(body, &response) != nil || !response.Success || response.Data == nil || response.Data.Total == nil || *response.Data.Total < 0 {
			break
		}
		if page == 1 {
			total = *response.Data.Total
			out.Requests = &total
		} else if total != *response.Data.Total {
			break
		}
		valid := true
		for _, row := range response.Data.Items {
			// Some providers reuse IDs for distinct consumption records. Compare
			// record contents across pages, while preserving rows within a page.
			fingerprint, _ := json.Marshal(row)
			key := string(fingerprint)
			if (seen[key] != 0 && seen[key] != page) || row.Type != 2 || row.Created < start || row.Created > end {
				valid = false
				break
			}
			seen[key] = page
			count++
			if row.Input == nil || *row.Input < 0 {
				inputComplete = false
			} else {
				input += *row.Input
			}
			if row.Output == nil || *row.Output < 0 {
				outputComplete = false
			} else {
				output += *row.Output
			}
		}
		if !valid {
			break
		}
		if count == total {
			if inputComplete {
				out.Input = &input
			}
			if outputComplete {
				out.Output = &output
			}
			if inputComplete && outputComplete {
				sum := input + output
				out.Total = &sum
			}
			return out
		}
		if len(response.Data.Items) == 0 || count > total {
			break
		}
	}
	// Do not display partial page totals as full daily usage. Cost from stat can
	// still be shown independently when log access fails or the bound is reached.
	if out.Cost == nil && out.Requests == nil {
		return nil
	}
	return out
}
