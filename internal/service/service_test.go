package service

import "testing"

func TestServiceStatus_String(t *testing.T) {
	tests := []struct {
		s    ServiceStatus
		want string
	}{
		{StatusRunning, "running"},
		{StatusStopped, "stopped"},
		{StatusNotInstalled, "not-installed"},
		{StatusUnknown, "unknown"},
	}
	for _, tt := range tests {
		if got := tt.s.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func TestNew(t *testing.T) {
	mgr := New("corelink")
	if mgr == nil {
		t.Fatal("New returned nil")
	}
}

func TestValidateServiceName(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"corelink", false},
		{"corelink-node", false},
		{"corelink_controller", false},
		{"CoreLink123", false},
		{"", true},
		{"bad name", true},
		{"bad\nname", true},
		{"bad;name", true},
		{"bad'name", true},
		{"../escape", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServiceName(tt.name)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateServiceName(%q) err=%v, wantErr=%v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestValidateServiceConfig(t *testing.T) {
	good := ServiceConfig{
		BinaryPath:  "/usr/bin/corelink",
		ConfigPath:  "/etc/corelink.json",
		DataDir:     "/var/lib/corelink",
		DisplayName: "CoreLink",
		Description: "CoreLink VPN",
	}
	if err := validateServiceConfig(good); err != nil {
		t.Fatalf("合法配置应通过校验: %v", err)
	}

	bad := good
	bad.BinaryPath = "/usr/bin/corelink\nExecStart=/bin/sh"
	if err := validateServiceConfig(bad); err == nil {
		t.Fatal("含换行的 BinaryPath 应被拒绝")
	}

	bad2 := good
	bad2.Args = []string{"--flag", "val\x00ue"}
	if err := validateServiceConfig(bad2); err == nil {
		t.Fatal("含空字节的 Args 应被拒绝")
	}
}
