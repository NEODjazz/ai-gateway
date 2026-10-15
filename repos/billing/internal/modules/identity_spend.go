package modules

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type IdentitySpendQuery struct {
	IdentityBudgetSummariesQuery
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

func (q IdentitySpendQuery) Valid() bool {
	return q.IdentityBudgetSummariesQuery.Valid() && !q.From.IsZero() && q.From.Unix() >= 0 && q.To.After(q.From) && q.To.Sub(q.From) <= 90*24*time.Hour
}

type IdentitySpend struct {
	ID       string  `json:"id"`
	Currency string  `json:"currency"`
	Cost     float64 `json:"cost"`
	Requests uint64  `json:"requests"`
}

// IdentitySpend uses finalized usage events, independently of budget reserves.
func (r *ClickHouseUsageReporter) IdentitySpend(ctx context.Context, q IdentitySpendQuery) ([]IdentitySpend, error) {
	if r == nil || r.client == nil || !q.Valid() {
		return nil, errors.New("invalid identity spend query")
	}
	column := "user_id"
	if q.ScopeType == "team" {
		column = "team_id"
	}
	parameters := map[string]string{"param_from": strconv.FormatInt(q.From.UTC().Unix(), 10), "param_to": strconv.FormatInt(q.To.UTC().Unix(), 10)}
	placeholders := make([]string, len(q.IDs))
	wanted := make(map[string]bool, len(q.IDs))
	for i, id := range q.IDs {
		name := "id_" + strconv.Itoa(i)
		parameters["param_"+name] = id
		placeholders[i] = "{" + name + ":String}"
		wanted[id] = true
	}
	where := fmt.Sprintf(`%s IN (%s) AND timestamp_unix >= {from:UInt64} AND timestamp_unix < {to:UInt64} AND phase IN ('commit','cancel')`, column, strings.Join(placeholders, ","))
	// Durable outbox delivery is at least once. Nonempty event IDs identify an
	// immutable payload; legacy rows without an event ID must remain independent.
	source := fmt.Sprintf(`(SELECT %s AS id,currency,cost FROM %s WHERE %s AND event_id=''
	 UNION ALL SELECT %s AS id,any(raw.currency) AS currency,any(raw.cost) AS cost FROM %s AS raw WHERE %s AND raw.event_id!='' GROUP BY raw.event_id,%s)`, column, r.table, where, column, r.table, where, column)
	parameters["query"] = fmt.Sprintf(`SELECT id,currency,sum(usage.cost) AS cost,count() AS requests FROM %s AS usage GROUP BY id,currency ORDER BY id,currency FORMAT JSONEachRow`, source)
	// Multipart parameters avoid URL limits for a complete 200-identity page.
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range parameters {
		if err := form.WriteField(name, value); err != nil {
			return nil, fmt.Errorf("encode identity spend query: %w", err)
		}
	}
	if err := form.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint+"/?output_format_json_quote_64bit_integers=0", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	if r.username != "" || r.password != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query identity spend: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("identity spend returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return nil, errors.New("identity spend response is incomplete or too large")
	}
	result := []IdentitySpend{}
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var row IdentitySpend
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("decode identity spend: %w", err)
		}
		key := row.ID + "\x00" + row.Currency
		if !wanted[row.ID] || seen[key] || len(row.Currency) != 3 || strings.IndexFunc(row.Currency, func(c rune) bool { return c < 'A' || c > 'Z' }) >= 0 || math.IsNaN(row.Cost) || math.IsInf(row.Cost, 0) || row.Cost < 0 || row.Requests == 0 {
			return nil, errors.New("invalid identity spend result")
		}
		seen[key] = true
		result = append(result, row)
	}
	return result, scanner.Err()
}
