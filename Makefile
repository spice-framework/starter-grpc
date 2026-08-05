.PHONY: acceptance check compatibility fmt verify verify-release

acceptance:
	go test -race -shuffle=on -count=1 .

check:
	go run ./internal/qualitygate -mode=check

compatibility:
	go run ./internal/qualitygate -mode=compatibility

fmt:
	go run ./internal/qualitygate -mode=fmt

verify:
	go run ./internal/qualitygate -mode=verify

verify-release:
	go run ./internal/qualitygate -mode=verify-release
