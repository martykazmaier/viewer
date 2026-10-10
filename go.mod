module bbsview

go 1.26.0

require (
	github.com/kpfaulkner/jxl-go v0.0.0-20260623205201-d6826b04db87
	github.com/sirupsen/logrus v1.9.3
	golang.org/x/image v0.47.0
)

require (
	golang.org/x/exp v0.0.0-20241004190924-225e2abe05e6 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

replace github.com/kpfaulkner/jxl-go => ./third_party/jxl-go
