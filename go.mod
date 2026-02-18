module golift.io/xtractr

go 1.25.6

toolchain go1.26.0

require (
	github.com/andybalholm/brotli v1.2.0
	github.com/bodgit/sevenzip v1.6.1
	github.com/cavaliergopher/cpio v1.0.1
	github.com/cavaliergopher/rpm v1.3.0
	github.com/dsnet/compress v0.0.1
	github.com/kdomanski/iso9660 v0.4.0
	github.com/klauspost/compress v1.18.4
	github.com/mewkiz/flac v1.0.13
	github.com/nwaples/rardecode/v2 v2.2.2
	github.com/peterebden/ar v0.0.0-20241106141004-20dc11b778e8
	github.com/pierrec/lz4/v4 v4.1.25
	github.com/saintfish/chardet v0.0.0-20230101081208-5e3ef4b5456d
	github.com/sshaman1101/dcompress v0.0.0-20200109162717-50436a6332de
	github.com/stretchr/testify v1.11.1
	github.com/therootcompany/xz v1.0.1
	github.com/ulikunitz/xz v0.5.15
	golang.org/x/text v0.34.0
	golift.io/udf v0.0.0-00010101000000-000000000000
)

require (
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/icza/bitio v1.1.0 // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/mewkiz/pkg v0.0.0-20250417130911-3f050ff8c56d // indirect
	github.com/mewpkg/term v0.0.0-20241026122259-37a80af23985 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/stretchr/objx v0.5.3 // indirect
	go4.org v0.0.0-20260112195520-a5071408f32f // indirect
	golang.org/x/crypto v0.48.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/kdomanski/iso9660 => github.com/Unpackerr/iso9660 v0.0.0-20260218033718-993fc9e3f4e7

// Remove after golift/udf#1 is merged and tagged.
replace golift.io/udf => github.com/golift/udf v0.0.0-20260218033433-9d14d60a73db
