cmake_minimum_required(VERSION 3.20)
project(ZoneClient LANGUAGES C CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

add_definitions(-DUNICODE -D_UNICODE -DWIN32_LEAN_AND_MEAN -DNOMINMAX)

find_package(Python3 COMPONENTS Interpreter REQUIRED)

# ----------------------------------------------------------------------
# Embedded Asset Generation
# ----------------------------------------------------------------------
set(GAMEDATA_DIR "${CMAKE_CURRENT_SOURCE_DIR}/../gamedata")
set(GEN_DIR "${CMAKE_CURRENT_BINARY_DIR}/generated")
file(MAKE_DIRECTORY "${GEN_DIR}")

set(GEN_ASSETS_SCRIPT "${CMAKE_CURRENT_SOURCE_DIR}/tools/generate_embedded_assets.py")
set(GEN_CPP "${GEN_DIR}/embedded_assets.cpp")
set(GEN_H "${GEN_DIR}/embedded_assets.h")

file(GLOB_RECURSE GAMEDATA_FILES
    "${GAMEDATA_DIR}/*.script"
    "${GAMEDATA_DIR}/*.xml"
    "${GAMEDATA_DIR}/*.ltx"
)

add_custom_command(
    OUTPUT "${GEN_CPP}" "${GEN_H}"
    COMMAND ${Python3_EXECUTABLE} "${GEN_ASSETS_SCRIPT}"
            "--gamedata" "${GAMEDATA_DIR}"
            "--out-cpp" "${GEN_CPP}"
            "--out-h" "${GEN_H}"
    DEPENDS ${GAMEDATA_FILES} "${GEN_ASSETS_SCRIPT}"
    COMMENT "Generating compile-time embedded assets from gamedata"
    VERBATIM
)

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
    src/hook/lua_hook.cpp
    src/hook/lua_hook.h
    src/identity/identity.cpp
    src/identity/identity.h
    src/protocol/packets.h
    src/net/udp_client.cpp
    src/net/udp_client.h
    src/lua/zone_bindings.cpp
    src/lua/zone_bindings.h
    src/provision/asset_provisioner.cpp
    src/provision/asset_provisioner.h
    3rdparty/lua/lua_bindings.cpp
    ${GEN_CPP}
    ${GEN_H}
)

add_library(ZoneClient SHARED ${ZONE_CLIENT_SOURCES})
target_include_directories(ZoneClient PRIVATE
    src
    3rdparty/minhook/include
    3rdparty/lua
    ${GEN_DIR}
)
target_link_libraries(ZoneClient PRIVATE
    minhook
    ws2_32
    shlwapi
    winmm
    wbemuuid
    rpcrt4
)
target_compile_definitions(ZoneClient PRIVATE ZO_INJECTABLE)

# ----------------------------------------------------------------------
# Target 2: ZoneClient_Injector (EXE)
# ----------------------------------------------------------------------
add_executable(ZoneClient_Injector injector/injector_main.cpp)
target_link_libraries(ZoneClient_Injector PRIVATE psapi rpcrt4)
