// Package plan persists the player's evolving strategy across save refreshes.
// The savegame is read-only and only reflects a frozen moment; the plan store
// is the server-owned memory of goals, build orders, and a turn-by-turn journal
// so Claude can track progress between saves.
package plan

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// mu serializes all plan read-modify-write cycles. MCP tool handlers run
// concurrently, and each mutating op is a load+modify+save; without this,
// rapid calls (e.g. seeding many objectives at once) clobber each other.
var mu sync.Mutex

// Status of an objective.
type Status string

const (
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
	StatusBlocked    Status = "blocked"
	StatusDropped    Status = "dropped"
)

// Objective is a single planned goal/build-order/task.
type Objective struct {
	ID       int      `json:"id"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail,omitempty"`
	Status   Status   `json:"status"`
	Priority int      `json:"priority,omitempty"` // lower = more urgent
	Tags     []string `json:"tags,omitempty"`
	Created  int64    `json:"created"`
	Updated  int64    `json:"updated"`
}

// JournalEntry is a timestamped note (e.g. "told player to buy energy cells").
type JournalEntry struct {
	Time  int64   `json:"time"`
	GameH float64 `json:"game_h,omitempty"` // in-game hours when noted, if known
	Note  string  `json:"note"`
}

// Plan is the full persisted strategy document for a single playthrough.
type Plan struct {
	PlaythroughID string         `json:"playthrough_id"`     // X4 game guid this plan belongs to
	Label         string         `json:"label,omitempty"`    // human label, e.g. player + start
	Strategy      string         `json:"strategy,omitempty"` // free-text overall direction
	Objectives    []Objective    `json:"objectives"`
	Journal       []JournalEntry `json:"journal"`
	NextID        int            `json:"next_id"`
	Created       int64          `json:"created"`
	Updated       int64          `json:"updated"`
}

// Store is a file-backed Plan for one playthrough.
type Store struct {
	id   string
	path string
	now  func() int64
}

// Dir returns the directory holding per-playthrough plan files
// (override with X4MCP_PLAN_DIR).
func Dir() string {
	if d := os.Getenv("X4MCP_PLAN_DIR"); d != "" {
		return d
	}
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		base, _ = os.UserHomeDir()
	}
	return filepath.Join(base, "x4mcp", "plans")
}

// sanitize makes a playthrough id safe as a file name.
func sanitize(id string) string {
	if id == "" {
		return "default"
	}
	keep := func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}
	return strings.Map(keep, id)
}

// Open returns the Store for a playthrough id (its X4 game guid). An empty id
// falls back to a shared "default" plan.
func Open(id string) *Store {
	return &Store{
		id:   id,
		path: filepath.Join(Dir(), sanitize(id)+".json"),
		now:  func() int64 { return time.Now().Unix() },
	}
}

// Meta summarizes a stored playthrough plan for listing.
type Meta struct {
	PlaythroughID string `json:"playthrough_id"`
	Label         string `json:"label,omitempty"`
	Strategy      string `json:"strategy,omitempty"`
	Objectives    int    `json:"objectives"`
	Updated       int64  `json:"updated"`
}

// List returns metadata for every stored playthrough plan.
func List() ([]Meta, error) {
	entries, err := os.ReadDir(Dir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Meta
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(Dir(), e.Name()))
		if err != nil {
			continue
		}
		var p Plan
		if json.Unmarshal(b, &p) != nil {
			continue
		}
		out = append(out, Meta{
			PlaythroughID: p.PlaythroughID,
			Label:         p.Label,
			Strategy:      p.Strategy,
			Objectives:    len(p.Objectives),
			Updated:       p.Updated,
		})
	}
	return out, nil
}

func (s *Store) load() (*Plan, error) {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &Plan{PlaythroughID: s.id, NextID: 1, Created: s.now()}, nil
	}
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("plan file %s is corrupt: %w", s.path, err)
	}
	if p.NextID == 0 {
		p.NextID = 1
	}
	if p.PlaythroughID == "" {
		p.PlaythroughID = s.id
	}
	return &p, nil
}

// EnsureMeta records the human label for this playthrough if not already set,
// creating the plan file on first contact. Idempotent.
func (s *Store) EnsureMeta(label string) (*Plan, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := s.load()
	if err != nil {
		return nil, err
	}
	if label != "" && p.Label != label {
		p.Label = label
		return p, s.save(p)
	}
	return p, nil
}

func (s *Store) save(p *Plan) error {
	p.Updated = s.now()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the current plan.
func (s *Store) Get() (*Plan, error) { return s.load() }

// SetStrategy replaces the free-text overall strategy.
func (s *Store) SetStrategy(text string) (*Plan, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := s.load()
	if err != nil {
		return nil, err
	}
	p.Strategy = text
	return p, s.save(p)
}

// AddObjective appends a new objective and returns the updated plan.
func (s *Store) AddObjective(title, detail string, priority int, tags []string) (*Plan, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := s.load()
	if err != nil {
		return nil, err
	}
	now := s.now()
	p.Objectives = append(p.Objectives, Objective{
		ID:       p.NextID,
		Title:    title,
		Detail:   detail,
		Status:   StatusTodo,
		Priority: priority,
		Tags:     tags,
		Created:  now,
		Updated:  now,
	})
	p.NextID++
	sortObjectives(p.Objectives)
	return p, s.save(p)
}

// UpdateObjective mutates an objective's status and/or detail. Empty/zero
// fields are left unchanged; status is applied when non-empty.
func (s *Store) UpdateObjective(id int, status Status, detail string, priority *int) (*Plan, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := s.load()
	if err != nil {
		return nil, err
	}
	found := false
	for i := range p.Objectives {
		if p.Objectives[i].ID != id {
			continue
		}
		found = true
		if status != "" {
			p.Objectives[i].Status = status
		}
		if detail != "" {
			p.Objectives[i].Detail = detail
		}
		if priority != nil {
			p.Objectives[i].Priority = *priority
		}
		p.Objectives[i].Updated = s.now()
	}
	if !found {
		return nil, fmt.Errorf("no objective with id %d", id)
	}
	sortObjectives(p.Objectives)
	return p, s.save(p)
}

// AddJournal appends a journal note.
func (s *Store) AddJournal(note string, gameH float64) (*Plan, error) {
	mu.Lock()
	defer mu.Unlock()
	p, err := s.load()
	if err != nil {
		return nil, err
	}
	p.Journal = append(p.Journal, JournalEntry{Time: s.now(), GameH: gameH, Note: note})
	return p, s.save(p)
}

func sortObjectives(objs []Objective) {
	sort.SliceStable(objs, func(i, j int) bool {
		// done/dropped sink to the bottom; otherwise by priority then id.
		ai, aj := terminal(objs[i].Status), terminal(objs[j].Status)
		if ai != aj {
			return !ai
		}
		if objs[i].Priority != objs[j].Priority {
			return objs[i].Priority < objs[j].Priority
		}
		return objs[i].ID < objs[j].ID
	})
}

func terminal(s Status) bool { return s == StatusDone || s == StatusDropped }
