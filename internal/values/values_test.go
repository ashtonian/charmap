package values

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolve(t *testing.T) {
	environ := []string{
		"PARAM_VARS_GROUP=us-east",
		"PARAM_VARS_CLUSTER_NAME=cluster-001",
		"GROUP=from-env",
		"EMPTY=",
		"SECRET_TOKEN=do-not-read",
		"=C:=windows-drive-entry",
	}
	tests := []struct {
		name string
		src  Sources
		want map[string]string
	}{
		{name: "nothing selected reads nothing", src: Sources{}, want: map[string]string{}},
		{
			name: "prefix strips the prefix",
			src:  Sources{EnvPrefix: "PARAM_VARS_"},
			want: map[string]string{"GROUP": "us-east", "CLUSTER_NAME": "cluster-001"},
		},
		{
			name: "named env vars, empty value allowed",
			src:  Sources{Env: []string{"GROUP", "EMPTY"}},
			want: map[string]string{"GROUP": "from-env", "EMPTY": ""},
		},
		{
			name: "set splits at the first equals and keeps commas",
			src:  Sources{Set: []string{"URL=https://x.example/?a=1&b=2", "LIST=a,b,c", "EMPTY="}},
			want: map[string]string{"URL": "https://x.example/?a=1&b=2", "LIST": "a,b,c", "EMPTY": ""},
		},
		{
			name: "precedence is prefix, then env, then set",
			src:  Sources{EnvPrefix: "PARAM_VARS_", Env: []string{"GROUP"}, Set: []string{"CLUSTER_NAME=override"}},
			want: map[string]string{"GROUP": "from-env", "CLUSTER_NAME": "override"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(tt.src, environ)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestResolveErrors(t *testing.T) {
	environ := []string{"GROUP=x", "PFX_bad-name=v", "PFX_=v"}
	tests := []struct {
		name    string
		src     Sources
		wantErr string
	}{
		{name: "env not set", src: Sources{Env: []string{"MISSING"}}, wantErr: "--env MISSING: environment variable is not set"},
		{name: "env invalid key", src: Sources{Env: []string{"a-b"}}, wantErr: "not a valid key"},
		{name: "env duplicate", src: Sources{Env: []string{"GROUP", "GROUP"}}, wantErr: "given more than once"},
		{name: "set without equals", src: Sources{Set: []string{"hunter2"}}, wantErr: "--set argument 1: expected KEY=VALUE"},
		{name: "set invalid key", src: Sources{Set: []string{"a-b=x"}}, wantErr: "not a valid key"},
		{name: "set empty key", src: Sources{Set: []string{"=x"}}, wantErr: "not a valid key"},
		{name: "set duplicate", src: Sources{Set: []string{"A=1", "A=2"}}, wantErr: "--set A given more than once"},
		{name: "prefix yields invalid key", src: Sources{EnvPrefix: "PFX_"}, wantErr: "is not a valid key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(tt.src, environ)
			assert.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestResolveErrorsNeverContainValues(t *testing.T) {
	_, err := Resolve(Sources{Set: []string{"hunter2"}}, nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "hunter2")
}
