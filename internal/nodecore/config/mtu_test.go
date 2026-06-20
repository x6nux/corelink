package config

import "testing"

func TestValidateMTU(t *testing.T) {
	tests := []struct {
		mtu     uint32
		wantErr bool
	}{
		{0, false},
		{1400, false},
		{1500, false},
		{9000, false},
		{65535, false},
		{1401, true},
		{2000, true},
		{8999, true},
	}
	for _, tt := range tests {
		err := ValidateMTU(tt.mtu)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateMTU(%d) err=%v, wantErr=%v", tt.mtu, err, tt.wantErr)
		}
	}
}

func TestDefaultMTU(t *testing.T) {
	if DefaultMTU != 1400 {
		t.Errorf("DefaultMTU = %d, want 1400", DefaultMTU)
	}
}

func TestResolveMTU(t *testing.T) {
	if ResolveMTU(0) != 1400 {
		t.Errorf("ResolveMTU(0) = %d, want 1400", ResolveMTU(0))
	}
	if ResolveMTU(9000) != 9000 {
		t.Errorf("ResolveMTU(9000) = %d, want 9000", ResolveMTU(9000))
	}
}
