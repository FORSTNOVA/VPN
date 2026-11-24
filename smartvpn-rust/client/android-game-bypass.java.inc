        try {
            JSONObject result = new JSONObject(CoreBridge.readGameBypass(getFilesDir().getAbsolutePath()));
            if (result.has("error")) return "无法应用游戏直连名单：" + result.getString("error");
            JSONArray games = result.getJSONArray("games");
            for (int i = 0; i < games.length(); i++) {
                String game = games.getString(i);
                if (!getPackageName().equals(game)) builder.addDisallowedApplication(game);
            }
        } catch (org.json.JSONException | android.content.pm.PackageManager.NameNotFoundException error) {
            return "无法应用游戏直连名单：" + error;
        }
