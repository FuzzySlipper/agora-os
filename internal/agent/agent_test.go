package agent

import "testing"

func TestParseNftHandle(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    uint64
		wantErr bool
	}{
		{
			name:  "typical echo output",
			input: "add rule inet filter agent-os-output meta skuid 60000 drop # handle 42\n",
			want:  42,
		},
		{
			name:  "large handle",
			input: "add rule inet filter agent-os-output meta skuid 60001 oif != \"lo\" drop # handle 9999\n",
			want:  9999,
		},
		{
			name:    "no handle",
			input:   "add rule inet filter agent-os-output meta skuid 60000 drop\n",
			wantErr: true,
		},
		{
			name:    "empty",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseNftHandle(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got handle %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}
