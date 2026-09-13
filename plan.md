1. The feedback noted that removing `<font>`, `<text_color>`, and other necessary attributes from `zone_ui_server_list.xml` inputs when copying from `asset_provisioner.cpp` broke the UI functionality.
2. I need to merge the layout structure from `asset_provisioner.cpp` into `zone_ui_server_list.xml` BUT preserve the detailed properties (like `<texture>`, `<font>`, `<text_color>`, `<max_symb_count>`, backward compatibility aliases, etc.) that existed in `zone_ui_server_list.xml`.
3. I also need to update `asset_provisioner.cpp` to embed this unified XML.
4. Update `zone_menu_patch.script` to load `btn_zone_online` from `zone_ui_server_list.xml`.
5. Verify changes with tests and code review.
