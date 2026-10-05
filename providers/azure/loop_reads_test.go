package azure

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// peakTracker counts concurrent requests whose path matches, while they wait.
type peakTracker struct {
	mu             sync.Mutex
	inFlight, peak int
}

func (p *peakTracker) hold(wait time.Duration) {
	p.mu.Lock()
	p.inFlight++
	p.peak = max(p.peak, p.inFlight)
	p.mu.Unlock()
	time.Sleep(wait)
	p.mu.Lock()
	p.inFlight--
	p.mu.Unlock()
}

func countCalls(s *dnsScenario, match func(string) bool, wait time.Duration) (map[string]int, *peakTracker) {
	var mu sync.Mutex
	calls, peak := map[string]int{}, &peakTracker{}
	s.before = func(req *http.Request) {
		path := strings.ToLower(req.URL.Path)
		mu.Lock()
		calls[req.Method+" "+path]++
		mu.Unlock()
		if match(path) {
			peak.hold(wait)
		}
	}
	return calls, peak
}

func dataCollectionAssociationScenario(t *testing.T, count int) (*dnsScenario, *client, string, []string) {
	t.Helper()
	s := newDNSScenario()
	rule := resourceID(dataCollectionRuleType, "rule")
	body := object(object(object(dataCollectionFixture(t, "DataCollectionRulesGet")["responses"])["200"])["body"])
	body["id"], body["name"], body["type"] = rule, "rule", dataCollectionRuleType
	s.add(body, dataCollectionVersion)
	collection := strings.ToLower(rule + "/associations")
	s.version[collection] = dataCollectionVersion
	var ids []string
	for i := range count {
		raw := object(object(object(dataCollectionFixture(t, "DataCollectionRuleAssociationsGet")["responses"])["200"])["body"])
		id := resourceID(vmType, fmt.Sprintf("vm%02d", i)) + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/a"
		raw["id"], raw["name"], raw["type"] = id, "a", dataCollectionAssociationType
		object(raw["properties"])["dataCollectionRuleId"] = rule
		s.add(raw, dataCollectionVersion)
		s.lists[collection] = append(s.lists[collection], raw)
		ids = append(ids, strings.ToLower(id))
	}
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return s, c, rule, ids
}

// One memo scope lists a rule's associations once for all of its members, and
// reads their GETs concurrently; a call without a memo (a delete check) reads
// live every time.
func TestDataCollectionAssociationsMemoizedAndConcurrent(t *testing.T) {
	s, c, rule, ids := dataCollectionAssociationScenario(t, 12)
	isMember := func(path string) bool {
		_, ok := s.records[path]
		return ok && strings.Contains(path, "/datacollectionruleassociations/")
	}
	calls, peak := countCalls(s, isMember, 5*time.Millisecond)
	parent := asset.Identity{NativeID: rule, NativeType: dataCollectionRuleType}
	list := "GET " + strings.ToLower(rule+"/associations")
	ctx := withReadMemo(t.Context())
	for range 3 {
		children, err := c.dataCollectionAssociations(ctx, parent)
		if err != nil || len(children) != len(ids) {
			t.Fatal("associations changed", len(children), err)
		}
		children[0].direct = false // A caller's edit must not reach the memo.
	}
	if again, _ := c.dataCollectionAssociations(ctx, parent); !again[0].direct {
		t.Fatal("caller mutated the memoized associations")
	}
	if calls[list] != 2 || calls["GET "+ids[0]] != 2 {
		t.Fatal("memo scope repeated association reads", calls[list], calls["GET "+ids[0]])
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("association reads were not concurrent within the bound", peak.peak)
	}
	clear(calls)
	for range 2 {
		if _, err := c.dataCollectionAssociations(t.Context(), parent); err != nil {
			t.Fatal(err)
		}
	}
	if calls[list] != 4 {
		t.Fatal("delete check reused a read", calls[list])
	}
}

// The first failure in row order wins, as in a serial walk: an earlier GET
// failure precedes a later invalid row, and rows after an invalid row are not read.
func TestDataCollectionAssociationsFirstErrorInOrder(t *testing.T) {
	s, c, rule, ids := dataCollectionAssociationScenario(t, 12)
	parent := asset.Identity{NativeID: rule, NativeType: dataCollectionRuleType}
	calls, _ := countCalls(s, func(string) bool { return false }, 0)
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.ToLower(req.URL.Path) == ids[2] {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		}
		return nil, false
	}
	listed := s.lists[strings.ToLower(rule+"/associations")]
	object(listed[7])["type"] = "Microsoft.Compute/disks"
	for range 5 {
		if _, err := c.dataCollectionAssociations(t.Context(), parent); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("later invalid row hid the earlier read failure", err)
		}
	}
	s.handle = nil
	object(listed[1])["type"] = "Microsoft.Compute/disks"
	clear(calls)
	if _, err := c.dataCollectionAssociations(t.Context(), parent); err == nil || !strings.Contains(err.Error(), "invalid_data_collection_association") {
		t.Fatal("invalid row accepted", err)
	}
	for _, id := range ids[1:] {
		if calls["GET "+id] != 0 {
			t.Fatal("read a row at or after the invalid row", id)
		}
	}
	if calls["GET "+ids[0]] != 1 {
		t.Fatal("did not read the row before the invalid row", calls["GET "+ids[0]])
	}
}

func privateDNSRecordsScenario(t *testing.T, count int) (*dnsScenario, *client, map[string]any, []string) {
	t.Helper()
	s := newDNSScenario()
	zoneID := resourceID(privateDNSZoneType, "internal.example.com")
	zone := map[string]any{"id": zoneID, "name": "internal.example.com", "location": "global", "etag": "zone-etag"}
	s.add(zone, "2024-06-01")
	collection := strings.ToLower(zoneID + "/A")
	var ids []string
	for i := range count {
		record := map[string]any{"id": fmt.Sprintf("%s/A/vm%02d", zoneID, i), "name": fmt.Sprintf("vm%02d", i), "etag": fmt.Sprint("etag", i), "properties": map[string]any{"ttl": 10}}
		s.add(record, "2024-06-01")
		s.lists[collection] = append(s.lists[collection], record)
		ids = append(ids, strings.ToLower(text(record["id"])))
	}
	c, err := s.runtime(t).resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return s, c, zone, ids
}

// Native cascade children are read concurrently and checked in list order.
func TestNativeServiceChildrenReadConcurrentlyInOrder(t *testing.T) {
	s, c, zone, ids := privateDNSRecordsScenario(t, 12)
	parent := asset.Identity{NativeID: text(zone["id"]), NativeType: privateDNSZoneType}
	kinds := []string{privateDNSZoneType + "/A"}
	isRecord := func(path string) bool { return strings.Contains(path, "/a/vm") }
	calls, peak := countCalls(s, isRecord, 5*time.Millisecond)
	children, err := c.nativeServiceChildren(t.Context(), parent, zone, kinds)
	if err != nil || len(children) != len(ids) {
		t.Fatal("children changed", len(children), err)
	}
	for _, id := range ids {
		if calls["GET "+id] != 1 {
			t.Fatal("child not read exactly once", id, calls["GET "+id])
		}
	}
	if peak.peak < 2 || peak.peak > detailReadConcurrency {
		t.Fatal("child reads were not concurrent within the bound", peak.peak)
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		switch strings.ToLower(req.URL.Path) {
		case ids[3]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied"}}, nil), true
		case ids[9]:
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied"}}, nil), true
		}
		return nil, false
	}
	for range 5 {
		if _, err := c.nativeServiceChildren(t.Context(), parent, zone, kinds); err == nil || !strings.Contains(err.Error(), "FirstDenied") {
			t.Fatal("first failure in list order lost", err)
		}
	}
	s.handle = nil
	listed := s.lists[strings.ToLower(text(zone["id"])+"/A")]
	listed[5] = listed[4] // A duplicate row fails after the rows before it.
	clear(calls)
	if _, err := c.nativeServiceChildren(t.Context(), parent, zone, kinds); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatal("duplicate child accepted", err)
	}
	for i, id := range ids {
		if want := map[bool]int{true: 1, false: 0}[i < 5]; calls["GET "+id] != want {
			t.Fatal("wrong reads around the invalid row", id, calls["GET "+id])
		}
	}
}

// readAhead (insights groups, key vaults, diagnostic contexts) reads each
// key once and looks results up in caller order; a key not started after an
// earlier failure is read at its own lookup.
func TestReadAheadFirstErrorInOrder(t *testing.T) {
	keys := make([]string, 20)
	for i := range keys {
		keys[i] = fmt.Sprint(i)
	}
	var reads sync.Map
	read := func(key string) (string, error) {
		n, _ := reads.LoadOrStore(key, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		time.Sleep(time.Millisecond)
		switch key {
		case "4":
			return "", fmt.Errorf("first")
		case "11":
			return "", fmt.Errorf("later")
		}
		return key, nil
	}
	lookup := readAhead(append(keys, keys...), read)
	var first error
	for _, key := range keys {
		value, err := lookup(key)
		if err != nil && first == nil {
			first = err
		}
		if err == nil && value != key {
			t.Fatal("lookup returned another key's value", key, value)
		}
	}
	if first == nil || first.Error() != "first" {
		t.Fatal("first error in order lost", first)
	}
	for _, key := range []string{"0", "4", "11"} {
		if n, ok := reads.Load(key); !ok || n.(*atomic.Int32).Load() != 1 {
			t.Fatal("key not read exactly once", key)
		}
	}
}

// Nested fan-outs (8 × 8 concurrent reads) never hold more than the client's
// round-trip slots at once; a canceled caller stops waiting for a slot.
func TestClientRoundTripCap(t *testing.T) {
	peak := &peakTracker{}
	c := directClient(func(req *http.Request) (*http.Response, error) {
		peak.hold(2 * time.Millisecond)
		return jsonResponse(200, map[string]any{"id": req.URL.Path}, nil), nil
	})
	c.inFlight = make(chan struct{}, clientRoundTrips)
	_, errs := readConcurrently(detailReadConcurrency, func(i int) ([]response, error) {
		reads, errs := readConcurrently(detailReadConcurrency*2, func(j int) (response, error) {
			return c.request(t.Context(), "GET", apiURL(fmt.Sprintf("%s/resourceGroups/g%d-%d", c.root(), i, j), resourcesVersion))
		})
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}
		return reads, nil
	})
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if peak.peak != clientRoundTrips {
		t.Fatal("round trips exceeded or never reached the client cap", peak.peak)
	}
	for range clientRoundTrips {
		c.inFlight <- struct{}{}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.request(ctx, "GET", apiURL(c.root()+"/resourceGroups/blocked", resourcesVersion)); err == nil {
		t.Fatal("request ran without a free slot")
	}
	if fresh, err := newClient(testCredential(), roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("unused") })); err != nil || cap(fresh.inFlight) != clientRoundTrips {
		t.Fatal("connection clients are not capped", err)
	}
}
