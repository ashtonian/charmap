module github.com/ashtonian/charmap/v2

go 1.26

// The Go that builds the released binaries and image (setup-go reads it);
// CI also tests with the oldest supported Go.
toolchain go1.27.4

require github.com/stretchr/testify v1.12.1

require go.yaml.in/yaml/v3 v3.0.5 // indirect
