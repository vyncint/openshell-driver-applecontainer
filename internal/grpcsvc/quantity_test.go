package grpcsvc

import "testing"

func TestParseCPUQuantity(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"2", 2, false},
		{"1.5", 2, false},
		{"500m", 1, false},
		{"1500m", 2, false},
		{"2000m", 2, false},
		{"  4  ", 4, false},
		{"-1", 0, true},
		{"abc", 0, true},
		{"1m1", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseCPUQuantity(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseCPUQuantity(%q) err = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseCPUQuantity(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestParseMemoryQuantityMB(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"", 0, false},
		{"0", 0, false},
		{"512Mi", 512, false},
		{"4Gi", 4096, false},
		{"1.5Gi", 1536, false},
		{"1Ti", 1 << 20, false},
		{"1G", 954, false},        // 1e9 bytes → ceil MiB
		{"268435456", 256, false}, // plain bytes
		{"1Ki", 1, false},         // rounds up to one MiB
		{"-5Mi", 0, true},
		{"lots", 0, true},
	}
	for _, tt := range tests {
		got, err := ParseMemoryQuantityMB(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseMemoryQuantityMB(%q) err = %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseMemoryQuantityMB(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
