package settings

import "testing"

func TestDefaultsValidate(t *testing.T) {
	if err := Validate(Config{}); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	got := ApplyDefaults(Config{})
	if got.MaxNestedDepth != DefaultMaxNestedDepth {
		t.Fatalf("max nested depth = %d", got.MaxNestedDepth)
	}
}

func TestHardLimitRejectsOutOfRange(t *testing.T) {
	cfg := Defaults()
	cfg.MaxNestedDepth = HardMaxNestedDepth + 1
	if err := Validate(cfg); err == nil {
		t.Fatal("expected max nested depth error")
	}
}
