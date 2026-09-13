cmake_minimum_required(VERSION 3.20)
project(ZoneClient LANGUAGES C CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

add_definitions(-DUNICODE -D_UNICODE -DWIN32_LEAN_AND_MEAN -DNOMINMAX)

# ----------------------------------------------------------------------
# 3rd Party: MinHook (static)
# ----------------------------------------------------------------------
set(MINHOOK_SOURCES
    3rdparty/minhook/src/buffer.c
    3rdparty/minhook/src/hook.c
    3rdparty/minhook/src/trampoline.c
    3rdparty/minhook/src/hde/hde64.c
)
add_library(minhook STATIC ${MINHOOK_SOURCES})
target_include_directories(minhook PUBLIC
    3rdparty/minhook/include
    3rdparty/minhook/src
)

# ----------------------------------------------------------------------
# Target 1: ZoneClient (SHARED injectable DLL)
# ----------------------------------------------------------------------
set(ZONE_CLIENT_SOURCES
    src/main.cpp
    src/identity/identity.cpp
    src/identity/identity.h
    src/protocol/packets.h
    src/net/udp_client.cpp
    src/net/udp_client.h
    src/lua/zone_bindings.cpp
    src/lua/zone_bindings.h
    src/provision/asset_provisioner.cpp
    src/provision/asset_provisioner.h
)

add_library(ZoneClient SHARED ${ZONE_CLIENT_SOURCES})
target_include_directories(ZoneClient PRIVATE
    src
    3rdparty/minhook/include
    3rdparty/lua
)
target_link_libraries(ZoneClient PRIVATE
    minhook
    ws2_32
    shlwapi
    winmm
    wbemuuid
)
target_compile_definitions(ZoneClient PRIVATE ZO_INJECTABLE)

# ----------------------------------------------------------------------
# Target 2: ZoneClient_Injector (EXE)
# ----------------------------------------------------------------------
add_executable(ZoneClient_Injector injector/injector_main.cpp)
target_link_libraries(ZoneClient_Injector PRIVATE psapi)
