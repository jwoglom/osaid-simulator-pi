PI ?= 10.55.0.1
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: admin admin-arm dev test prebuild image push push-sims clean

admin:            ## build the admin for this machine
	cd admin && go build -ldflags "-X main.version=$(VERSION)" -o osaid-admin .

admin-arm:        ## cross-compile the admin for the Pi (ARMv6, runs on Zero W and Zero 2 W)
	cd admin && GOOS=linux GOARCH=arm GOARM=6 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o osaid-admin-armv6 .

dev:              ## run the admin on this machine in mock mode: http://127.0.0.1:8080/
	cd admin && go run . -mock

test:
	cd admin && gofmt -l . && go vet ./... && go test ./...

prebuild:         ## download toolchain/JDK/jar and cross-compile the pinned simulators
	image/prebuild.sh

image: prebuild   ## build the SD card image with pi-gen in Docker -> image/deploy/
	image/build.sh

push: admin-arm   ## replace the admin on a running Pi over the USB link and restart it
	curl -sf -F binary=@admin/osaid-admin-armv6 http://$(PI)/api/admin/upload && echo

push-sims:        ## replace a running Pi's osaid.yaml with the default, then upload and build each simulator's branch head
	PI=$(PI) scripts/push-sims.sh

clean:
	rm -rf admin/osaid-admin admin/osaid-admin-armv6 admin/dev-state image/deploy
