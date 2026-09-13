#include "asset_provisioner.h"
#include <windows.h>
#include <shlwapi.h>
#include <string>
#include <fstream>
#include <vector>

#pragma comment(lib, "shlwapi.lib")

namespace
{
    std::wstring GetGameRoot()
    {
        wchar_t buf[MAX_PATH] = {};
        GetModuleFileNameW(nullptr, buf, MAX_PATH);
        std::wstring path(buf);
        
        // Strip executable name
        size_t last = path.find_last_of(L"\\/");
        if (last != std::wstring::npos)
            path = path.substr(0, last);
            
        // If executable is in "bin" or "bin\x64" or similar, go up to game root
        last = path.find_last_of(L"\\/");
        if (last != std::wstring::npos)
        {
            std::wstring folder = path.substr(last + 1);
            if (_wcsicmp(folder.c_str(), L"bin") == 0 ||
                _wcsicmp(folder.c_str(), L"bin_x64") == 0 ||
                _wcsicmp(folder.c_str(), L"bin_dedicated") == 0)
            {
                path = path.substr(0, last);
            }
        }
        return path;
    }

    bool FileExists(const std::wstring& path)
    {
        DWORD attrib = GetFileAttributesW(path.c_str());
        return (attrib != INVALID_FILE_ATTRIBUTES && !(attrib & FILE_ATTRIBUTE_DIRECTORY));
    }

    bool EnsureDirectoryTree(const std::wstring& dirPath)
    {
        if (dirPath.empty()) return true;
        
        DWORD attrib = GetFileAttributesW(dirPath.c_str());
        if (attrib != INVALID_FILE_ATTRIBUTES && (attrib & FILE_ATTRIBUTE_DIRECTORY))
            return true;

        size_t slash = dirPath.find_last_of(L"\\/");
        if (slash != std::wstring::npos)
        {
            EnsureDirectoryTree(dirPath.substr(0, slash));
        }

        return CreateDirectoryW(dirPath.c_str(), nullptr) || GetLastError() == ERROR_ALREADY_EXISTS;
    }

    bool WriteFileIfMissing(const std::wstring& path, const std::string& content)
    {
        if (FileExists(path))
            return true; // Already exists, don't overwrite user customizations

        size_t slash = path.find_last_of(L"\\/");
        if (slash != std::wstring::npos)
        {
            EnsureDirectoryTree(path.substr(0, slash));
        }

        std::ofstream file(path, std::ios::out | std::ios::binary);
        if (!file.is_open())
            return false;

        file.write(content.data(), content.size());
        file.close();
        return true;
    }

    const char* g_DLTX_ModSystem = 
        "; Zone DLTX Patch — auto-merged into system.ltx by Anomaly Modded EXEs\n"
        "; Adds proxy stalker NPC section used for remote player avatars\n"
        "\n"
        "[zone_proxy_stalker]:sim_default_stalker\n"
        "$spawn                              = \"respawn\\zone_proxy_stalker\"\n"
        "species                             = stalker\n"
        "community                           = stalker\n"
        "character_name                      = none\n"
        "clan                                = stalker\n"
        "initial_reputation                  = 400\n"
        "use_simplified_phys_while_not_active = true\n";

    const char* g_UI_ServerListXML = 
"<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"
        "<w x=\"0\" y=\"0\" width=\"1024\" height=\"768\">\n"
        "    <background x=\"163\" y=\"84\" width=\"698\" height=\"600\" stretch=\"1\">\n"
        "        <texture>ui\\ui_actor_multiplayer_game_menu</texture>\n"
        "    </background>\n"
        "    \n"
        "    <header_label x=\"200\" y=\"100\" width=\"624\" height=\"30\" font=\"letterica18\" align=\"c\" color=\"gold\">\n"
        "        <text>ZONE ONLINE — SERVER BROWSER</text>\n"
        "    </header_label>\n"
        "    \n"
        "    <server_list_frame x=\"175\" y=\"140\" width=\"674\" height=\"250\">\n"
        "        <col_name x=\"0\" y=\"0\" width=\"280\" height=\"20\" font=\"letterica16\"><text>Server Name</text></col_name>\n"
        "        <col_host x=\"280\" y=\"0\" width=\"220\" height=\"20\" font=\"letterica16\"><text>Host / IP</text></col_host>\n"
        "        <col_port x=\"500\" y=\"0\" width=\"80\" height=\"20\" font=\"letterica16\"><text>Port</text></col_port>\n"
        "        <col_status x=\"580\" y=\"0\" width=\"80\" height=\"20\" font=\"letterica16\"><text>Status</text></col_status>\n"
        "        <list id=\"server_list\" x=\"0\" y=\"22\" width=\"674\" height=\"228\" item_height=\"22\" show_by_need=\"1\">\n"
        "            <font font=\"letterica16\" r=\"200\" g=\"200\" b=\"200\" />\n"
        "            <text_color>\n"
        "                <e r=\"100\" g=\"220\" b=\"255\" />\n"
        "                <d r=\"140\" g=\"140\" b=\"140\" />\n"
        "            </text_color>\n"
        "        </list>\n"
        "    </server_list_frame>\n"
        "    \n"
        "    <direct_label x=\"175\" y=\"400\" width=\"300\" height=\"22\" font=\"letterica16\"><text>Direct Connect:</text></direct_label>\n"
        "    <ip_label x=\"175\" y=\"428\" width=\"80\" height=\"22\" font=\"letterica16\"><text>IP Address:</text></ip_label>\n"
        "    <edit_ip id=\"edit_ip\" x=\"260\" y=\"428\" width=\"240\" height=\"24\">\n"
        "        <texture>ui_inGame2_edit_box</texture>\n"
        "        <font font=\"letterica16\" />\n"
        "        <text_color r=\"230\" g=\"230\" b=\"230\" />\n"
        "        <max_symb_count>64</max_symb_count>\n"
        "    </edit_ip>\n"
        "    <ip_edit id=\"ip_edit\" x=\"260\" y=\"428\" width=\"240\" height=\"24\">\n"
        "        <texture>ui_inGame2_edit_box</texture>\n"
        "        <font font=\"letterica16\" />\n"
        "        <text_color r=\"230\" g=\"230\" b=\"230\" />\n"
        "        <max_symb_count>64</max_symb_count>\n"
        "    </ip_edit>\n"
        "    \n"
        "    <port_label x=\"175\" y=\"458\" width=\"80\" height=\"22\" font=\"letterica16\"><text>Port:</text></port_label>\n"
        "    <edit_port id=\"edit_port\" x=\"260\" y=\"458\" width=\"100\" height=\"24\">\n"
        "        <texture>ui_inGame2_edit_box</texture>\n"
        "        <font font=\"letterica16\" />\n"
        "        <text_color r=\"230\" g=\"230\" b=\"230\" />\n"
        "        <max_symb_count>5</max_symb_count>\n"
        "    </edit_port>\n"
        "    <port_edit id=\"port_edit\" x=\"260\" y=\"458\" width=\"100\" height=\"24\">\n"
        "        <texture>ui_inGame2_edit_box</texture>\n"
        "        <font font=\"letterica16\" />\n"
        "        <text_color r=\"230\" g=\"230\" b=\"230\" />\n"
        "        <max_symb_count>5</max_symb_count>\n"
        "    </port_edit>\n"
        "    \n"
        "    <nick_label x=\"175\" y=\"488\" width=\"80\" height=\"22\" font=\"letterica16\"><text>Callsign:</text></nick_label>\n"
        "    <edit_nick id=\"edit_nick\" x=\"260\" y=\"488\" width=\"200\" height=\"24\">\n"
        "        <texture>ui_inGame2_edit_box</texture>\n"
        "        <font font=\"letterica16\" />\n"
        "        <text_color r=\"230\" g=\"230\" b=\"230\" />\n"
        "        <max_symb_count>32</max_symb_count>\n"
        "    </edit_nick>\n"
        "    <nick_edit id=\"nick_edit\" x=\"260\" y=\"488\" width=\"200\" height=\"24\">\n"
        "        <texture>ui_inGame2_edit_box</texture>\n"
        "        <font font=\"letterica16\" />\n"
        "        <text_color r=\"230\" g=\"230\" b=\"230\" />\n"
        "        <max_symb_count>32</max_symb_count>\n"
        "    </nick_edit>\n"
        "    \n"
        "    <btn_connect id=\"btn_connect\" x=\"175\" y=\"524\" width=\"140\" height=\"36\" font=\"letterica18\">\n"
        "        <text>Connect</text>\n"
        "        <texture>ui\\ui_common:ui_inGame2_button_e</texture>\n"
        "    </btn_connect>\n"
        "    \n"
        "    <btn_add_fav id=\"btn_add_fav\" x=\"325\" y=\"524\" width=\"140\" height=\"36\" font=\"letterica18\">\n"
        "        <text>Add Favorite</text>\n"
        "        <texture>ui\\ui_common:ui_inGame2_button_e</texture>\n"
        "    </btn_add_fav>\n"
        "    \n"
        "    <btn_del_fav id=\"btn_del_fav\" x=\"475\" y=\"524\" width=\"155\" height=\"36\" font=\"letterica18\">\n"
        "        <text>Remove Fav</text>\n"
        "        <texture>ui\\ui_common:ui_inGame2_button_e</texture>\n"
        "    </btn_del_fav>\n"
        "    \n"
        "    <btn_back id=\"btn_back\" x=\"175\" y=\"572\" width=\"140\" height=\"36\" font=\"letterica18\">\n"
        "        <text>Back</text>\n"
        "        <texture>ui\\ui_common:ui_inGame2_button_e</texture>\n"
        "    </btn_back>\n"
        "\n"
        "    <btn_zone_online x=\"40\" y=\"548\" width=\"215\" height=\"30\">\n"
        "        <texture>ui\\ui_common:ui_inGame2_button_e</texture>\n"
        "        <text font=\"letterica18\" r=\"255\" g=\"255\" b=\"255\">Zone Online</text>\n"
        "    </btn_zone_online>\n"
        "</w>\n"
        "\n";
}

namespace AssetProvisioner
{
    void EnsureAssets()
    {
        std::wstring root = GetGameRoot();
        if (root.empty()) return;

        // 1. Provision DLTX mod_system_zone_online.ltx
        std::wstring dltxPath = root + L"\\gamedata\\configs\\mod_system_zone_online.ltx";
        WriteFileIfMissing(dltxPath, g_DLTX_ModSystem);

        // 2. Provision UI XML zone_ui_server_list.xml
        std::wstring xmlPath = root + L"\\gamedata\\configs\\ui\\zone_ui_server_list.xml";
        WriteFileIfMissing(xmlPath, g_UI_ServerListXML);
    }
}