#pragma once
#include <string>

namespace AssetProvisioner
{
    // Checks the S.T.A.L.K.E.R. Anomaly game root and automatically provisions
    // any missing configuration (DLTX mod_system_zone_online.ltx), UI XML
    // (zone_ui_server_list.xml), and essential scripts.
    void EnsureAssets();
    
    // Returns the path to the game root directory
    std::wstring GetGameRoot();
}