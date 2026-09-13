# ============================================================
#  Zone - Root Build Orchestrator
#  Usage:
#    make server        - build zone-server.exe (Windows)
#    make server-linux  - cross-compile zone-server (Linux amd64)
#    make client        - cmake build ZoneClient.dll + ZoneClient_Injector
#    make client-config - configure cmake (run once before client)
#    make test          - run Go server unit tests
#    make dist          - assemble dist/ release package
#    make all           - server + client
#    make clean         - remove build artifacts
# ============================================================

VERSION ?= 0.1.0
DIST_DIR := dist/zone-online-v$(VERSION)

.PHONY: all server server-linux client client-config test dist clean

all: server client

server:
	@echo "[*] Building zone-server (Windows)..."
	$(MAKE) -C zone-server build

server-linux:
	@echo "[*] Building zone-server (Linux amd64)..."
	$(MAKE) -C zone-server build-linux

test:
	@echo "[*] Running Go unit tests..."
	$(MAKE) -C zone-server test

client-config:
	@echo "[*] Configuring zone-client with CMake..."
	cmake -S zone-client -B zone-client/build -G "Visual Studio 17 2022" -A x64 -DCMAKE_BUILD_TYPE=Release
	@echo "[+] CMake configured. Run 'make client' to build."

client:
	@echo "[*] Building zone-client (Release)..."
	cmake --build zone-client/build --config Release --parallel
	@echo "[+] DLLs built: zone-client/build/Release/"

dist: server client
	@echo "[*] Assembling distribution package..."
	@mkdir -p $(DIST_DIR)/server
	@mkdir -p $(DIST_DIR)/client/bin
	@mkdir -p $(DIST_DIR)/gamedata/scripts
	@mkdir -p $(DIST_DIR)/gamedata/configs
	@mkdir -p $(DIST_DIR)/gamedata/configs/ui
	@cp zone-server/zone-server.exe $(DIST_DIR)/server/
	@cp zone-server/zone_server.yaml $(DIST_DIR)/server/
	@cp zone-client/build/Release/ZoneClient.dll $(DIST_DIR)/client/bin/
	@cp zone-client/build/Release/ZoneClient_Injector.exe $(DIST_DIR)/client/bin/
	@cp gamedata/scripts/*.script $(DIST_DIR)/gamedata/scripts/
	@cp gamedata/configs/*.ltx $(DIST_DIR)/gamedata/configs/
	@cp -r gamedata/configs/ui/* $(DIST_DIR)/gamedata/configs/ui/
	@cp LICENSE $(DIST_DIR)/
	@cp VERSION.txt $(DIST_DIR)/
	@cp INSTALL.md $(DIST_DIR)/
	@echo "[+] Distribution assembled at $(DIST_DIR)/"

clean:
	@echo "[*] Cleaning build artifacts..."
	$(MAKE) -C zone-server clean 2>/dev/null || true
	@rm -rf zone-client/build/
	@rm -rf dist/zone-online-* dist/*.zip
	@echo "[+] Clean complete."