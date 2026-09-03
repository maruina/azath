package alerts_test

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ruleFile mirrors the top-level structure of a Prometheus alerting rules file.
type ruleFile struct {
	Groups []ruleGroup `yaml:"groups"`
}

type ruleGroup struct {
	Name  string      `yaml:"name"`
	Rules []alertRule `yaml:"rules"`
}

type alertRule struct {
	Alert       string            `yaml:"alert"`
	Expr        string            `yaml:"expr"`
	For         string            `yaml:"for"`
	Labels      map[string]string `yaml:"labels"`
	Annotations map[string]string `yaml:"annotations"`
}

func loadAlertFile(t *testing.T) ruleFile {
	t.Helper()
	data, err := os.ReadFile("azath.yml")
	if err != nil {
		t.Fatalf("reading azath.yml: %v", err)
	}
	var rf ruleFile
	if err := yaml.Unmarshal(data, &rf); err != nil {
		t.Fatalf("parsing azath.yml: %v", err)
	}
	return rf
}

func TestAlertRules_ValidYAML(t *testing.T) {
	t.Parallel()
	rf := loadAlertFile(t)
	if len(rf.Groups) == 0 {
		t.Fatal("expected at least one rule group")
	}
}

func TestAlertRules_ExpectedAlerts(t *testing.T) {
	t.Parallel()
	rf := loadAlertFile(t)

	expected := []string{
		"AzathMasterKeyNotLoaded",
		"AzathUnsealFailureRateHigh",
		"AzathNotificationFailure",
		"AzathGateAPIError",
		"AzathRegistryLoadError",
		"AzathUnsealRateSpike",
	}

	found := make(map[string]struct{})
	for _, group := range rf.Groups {
		for _, rule := range group.Rules {
			found[rule.Alert] = struct{}{}
		}
	}

	for _, name := range expected {
		if _, ok := found[name]; !ok {
			t.Errorf("expected alert %q not found in azath.yml", name)
		}
	}
}

// TestAlertRules_RatioExpressionsUseSumWrapper verifies that alerts using
// division have sum() wrappers on both sides. Without sum(), Prometheus matches
// series by label set — each series divides itself → ratio is always 1.0.
func TestAlertRules_RatioExpressionsUseSumWrapper(t *testing.T) {
	t.Parallel()
	rf := loadAlertFile(t)
	for _, group := range rf.Groups {
		for _, rule := range group.Rules {
			t.Run(rule.Alert, func(t *testing.T) {
				t.Parallel()
				if !strings.Contains(rule.Expr, "/") {
					return // not a ratio alert
				}
				if !strings.Contains(rule.Expr, "sum(") {
					t.Errorf("alert %q: ratio expression missing sum() wrapper: %s", rule.Alert, rule.Expr)
				}
			})
		}
	}
}

func TestAlertRules_RequiredFields(t *testing.T) {
	t.Parallel()
	rf := loadAlertFile(t)

	for _, group := range rf.Groups {
		for _, rule := range group.Rules {
			t.Run(rule.Alert, func(t *testing.T) {
				t.Parallel()
				if rule.Alert == "" {
					t.Error("alert name is empty")
				}
				if rule.Expr == "" {
					t.Errorf("alert %q: expr is empty", rule.Alert)
				}
				if rule.Labels["severity"] == "" {
					t.Errorf("alert %q: missing labels.severity", rule.Alert)
				}
				if rule.Annotations["summary"] == "" {
					t.Errorf("alert %q: missing annotations.summary", rule.Alert)
				}
			})
		}
	}
}
