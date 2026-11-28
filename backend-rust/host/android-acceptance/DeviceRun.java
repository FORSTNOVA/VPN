package com.smartvpn.acceptance;

import android.app.*;
import android.content.*;
import android.net.VpnService;
import android.os.*;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.util.UUID;
import org.json.JSONObject;

public final class DeviceRun extends Instrumentation {
    private Context app;
    private Class<?> bridge;
    private JSONObject bootstrap;
    private boolean requireReadHost;
    private boolean requireWriteHost;
    private boolean testLiveMerge;
    private String nodeLink;
    private String nodeType;
    private String storeStep;
    private String storeFixture;
    private String distributionBackend;
    private static final String NAME = "phase61-" + UUID.randomUUID().toString().substring(0, 8);
    @Override
    public void onCreate(Bundle args) {
        super.onCreate(args);
        if (args != null) {
            requireWriteHost = "1".equals(args.getString("writeHost"));
            testLiveMerge = "1".equals(args.getString("liveMerge"));
            requireReadHost = "1".equals(args.getString("readHost"));
            nodeLink = args.getString("nodeLink64");
            nodeType = args.getString("nodeType");
            storeStep = args.getString("storeStep");
            storeFixture = args.getString("storeFixture");
            distributionBackend = args.getString("distributionBackend");
        }
        start();
    }
    private static void check(boolean ok, String message) throws IOException {
        if (!ok)
            throw new IOException(message);
    }
    private static String read(InputStream in) throws IOException {
        try (BufferedReader reader =
                 new BufferedReader(new InputStreamReader(in, StandardCharsets.UTF_8))) {
            StringBuilder b = new StringBuilder();
            for (String line; (line = reader.readLine()) != null;) b.append(line).append('\n');
            return b.toString();
        }
    }
    private String shell(String command) throws IOException {
        return read(new ParcelFileDescriptor.AutoCloseInputStream(
            getUiAutomation().executeShellCommand(command)));
    }
    private JSONObject api(String method, String path, JSONObject body) throws Exception {
        URL url = new URL("http://127.0.0.1:" + bootstrap.getInt("port") + "/api/" + path);
        HttpURLConnection c = (HttpURLConnection) url.openConnection(Proxy.NO_PROXY);
        try {
            // Flutter opens a fresh HttpClient for each local API request.
            c.setRequestProperty("Connection", "close");
            c.setRequestMethod(method);
            c.setConnectTimeout(3000);
            c.setReadTimeout(60000);
            c.setRequestProperty("Authorization", "Bearer " + bootstrap.getString("token"));
            if (body != null) {
                c.setDoOutput(true);
                c.setRequestProperty("Content-Type", "application/json");
                try (OutputStream out = c.getOutputStream()) {
                    out.write(body.toString().getBytes(StandardCharsets.UTF_8));
                }
            }
            int status = c.getResponseCode();
            if (status != 200)
                throw new IOException(path + " HTTP " + status + " " + read(c.getErrorStream()));
            return new JSONObject(read(c.getInputStream()));
        } finally {
            c.disconnect();
        }
    }
    private Intent vpn() {
        return new Intent().setClassName(app, "com.smartvpn.client.SmartVpnService");
    }
    private JSONObject tunnel() throws Exception {
        return new JSONObject((String) bridge.getMethod("describeTunnelNow").invoke(null));
    }
    private void awaitTunnel(boolean up) throws Exception {
        for (int n = 0; n < 100; n++) {
            if ((!tunnel().optString("interface").isEmpty()) == up) {
                if (up) {
                    // Android publishes the interface before setTunnel hands
                    // its descriptor and permission to the native backend.
                    JSONObject state = api("GET", "tun", null);
                    if (state.getBoolean("elevated") && state.optInt("descriptor") > 0)
                        return;
                } else if (!api("GET", "identify", null).getBoolean("connected")) {
                    return;
                }
            }
            Thread.sleep(100);
        }
        throw new IOException("tunnel state timeout: " + up);
    }
    private void peer(Fixture node, String action) throws Exception {
        String nonce = UUID.randomUUID().toString();
        if (node != null) node.nonce = nonce;
        // A previous peer may have logged its result just before finish() ran.
        // Give each challenge its own task so Android always invokes onCreate.
        String started = shell("am start -f 0x18000000 -n com.smartvpn.acceptance/.Peer --es nonce " + nonce
            + " --ez " + action + " true");
        check(started.contains("Starting: Intent") && !started.contains("Error") && !started.contains("not started"), started);
        for (int n = 0; n < 150; n++) {
            String log = shell("logcat -d -s SmartVPNPeer:I");
            if (log.contains(nonce + " PASS"))
                return;
            if (log.contains(nonce))
                throw new IOException(log.substring(log.indexOf(nonce)).trim());
            Thread.sleep(200);
        }
        throw new IOException("peer timed out");
    }
    // Run only on a disposable Android Studio AVD with the paired protocol fixture.
    private void checkNodeLink() throws Exception {
        check("vmess".equals(nodeType) || "tuic".equals(nodeType), "invalid test protocol");
        String uri = new String(android.util.Base64.decode(nodeLink, android.util.Base64.URL_SAFE), StandardCharsets.UTF_8);
        api("PUT", "settings", new JSONObject().put("subscriptionUrl", uri));
        org.json.JSONArray nodes = api("POST", "nodes/fetch", null).getJSONArray("nodes");
        String expected = "AVD-" + ("vmess".equals(nodeType) ? "VMess" : "TUIC");
        boolean imported = false;
        for (int i = 0; i < nodes.length(); i++) {
            JSONObject node = nodes.getJSONObject(i);
            if (expected.equals(node.getString("name")) && nodeType.equals(node.getString("type"))) imported = true;
        }
        check(imported, "single node missing after fetch");
        api("PUT", "nodes/select", new JSONObject().put("name", expected));
        check(VpnService.prepare(app) == null, "emulator VPN consent required");
        app.startForegroundService(vpn().setAction("com.smartvpn.client.START"));
        for (int i = 0; i < 100 && api("GET", "tun", null).getString("interface").isEmpty(); i++) Thread.sleep(100);
        api("POST", "connect", null);
        try {
            JSONObject state = api("GET", "state", null);
            check(state.getBoolean("connected") && expected.equals(state.getString("selectedNode")), "wrong active node");
            peer(null, "packetTest");
            report(expected + " imported; active VPN; TCP 32768 and UDP 1024 bytes verified in both directions");
        } finally {
            api("POST", "disconnect", null);
        }
    }
    private boolean workerRunning(String name) throws IOException {
        File[] tasks = new File("/proc/self/task").listFiles();
        if (tasks == null)
            throw new IOException("cannot enumerate own threads");
        for (File task : tasks) {
            try {
                if (name.equals(
                        read(new FileInputStream(new File(task, "comm"))).trim()))
                    return true;
            } catch (FileNotFoundException gone) { /* worker exited during enumeration */
            }
        }
        return false;
    }
    private void report(String line) {
        Bundle b = new Bundle();
        b.putString("stream", line + "\n");
        sendStatus(0, b);
    }
    private boolean hasNode(String route, String name) throws Exception {
        org.json.JSONArray nodes = api("GET", route, null).getJSONArray("nodes");
        for (int i = 0; i < nodes.length(); i++) {
            if (name.equals(nodes.getJSONObject(i).getString("name")))
                return true;
        }
        return false;
    }
    // Use the same random fixture across signed APK upgrades and process restarts.
    private void checkStoreUpgrade() throws Exception {
        String id = UUID.fromString(storeFixture).toString();
        String name = "store-test-" + id;
        String uri = "vless://" + id + "@127.0.0.1:9?security=none&type=tcp#" + name;
        if (storeStep.startsWith("profile-")) {
            checkProfileUpgrade(name, uri);
            return;
        }
        boolean found = hasNode("nodes/manual", name);
        switch (storeStep) {
        case "seed":
            check(!found, "test fixture already exists");
            api("POST", "nodes/manual", new JSONObject().put("name", name).put("uri", uri));
            break;
        case "remove":
            check(found, "persisted test fixture missing after upgrade");
            api("DELETE", "nodes/manual?name=" + name, null);
            break;
        case "check-absent":
            check(!found, "deleted test fixture reappeared after restart");
            break;
        default:
            throw new IOException("unknown store test step");
        }
        report("store test passed: " + storeStep);
    }

    private void checkProfileUpgrade(String name, String uri) throws Exception {
        org.json.JSONArray hops = new org.json.JSONArray().put(name).put(name + "-2");
        if ("profile-seed".equals(storeStep)) {
            api("POST", "nodes/manual", new JSONObject().put("name", name).put("uri", uri));
            api("POST", "nodes/manual", new JSONObject().put("name", name + "-2").put("uri", uri + "-2"));
            api("PUT", "settings", new JSONObject().put("domesticWeb", true));
            api("PUT", "nodes/chain/profile", new JSONObject().put("name", name).put("hops", hops));
            api("POST", "subscriptions/add", new JSONObject().put("url", uri).put("label", name));
        } else {
            boolean upgraded = "profile-upgrade".equals(storeStep);
            check(upgraded || "profile-rollback".equals(storeStep), "unknown profile step");
            boolean mapped = read(new FileInputStream("/proc/self/maps")).contains("libsmartvpn_store.so");
            check(mapped == upgraded, "unexpected SQLite backend library");
            JSONObject state = api("GET", "state", null);
            check(state.getBoolean("domesticWeb") == upgraded, "settings lost");
            check(state.getJSONObject("chains").getJSONArray(name).toString().equals(hops.toString()), "chain changed");
            check(hasNode("nodes/manual", name) && hasNode("nodes/manual", name + "-2"), "manual nodes lost");
            org.json.JSONArray subs = api("GET", "subscriptions", null).getJSONArray("subscriptions");
            String sid = null;
            for (int i = 0; i < subs.length(); i++) {
                JSONObject sub = subs.getJSONObject(i);
                if (uri.equals(sub.getString("url"))) {
                    check(sub.getString("label").equals(upgraded ? name : name + "-updated"), "subscription label changed");
                    sid = sub.getString("id");
                }
            }
            check(sid != null, "subscription lost");
            JSONObject history = api("GET", "traffic/history?period=day&date=2002-01-02&app=" + name, null);
            check(history.getLong("uploadTotal") == 123 && history.getLong("downloadTotal") == 456, "history changed");
            if (upgraded) {
                api("POST", "subscriptions/update", new JSONObject().put("id", sid).put("label", name + "-updated"));
                api("PUT", "settings", new JSONObject().put("domesticWeb", false));
            }
        }
        report("store test passed: " + storeStep);
    }
    @Override
    public void onStart() {
        Bundle result = new Bundle();
        boolean passed = false, added = false;
        String subscriptionId = null;
        try (Fixture node = storeStep == null && nodeLink == null ? new Fixture() : null) {
            app = getTargetContext();
            if ("profile-history".equals(storeStep)) {
                // Populate one deterministic row in the previous APK's closed database.
                String name = "store-test-" + UUID.fromString(storeFixture);
                File db = new File(app.getFilesDir(), "SmartVPN/smartvpn.db");
                try (android.database.sqlite.SQLiteDatabase old = android.database.sqlite.SQLiteDatabase.openDatabase(
                        db.getPath(), null, android.database.sqlite.SQLiteDatabase.OPEN_READWRITE)) {
                    old.execSQL("INSERT INTO traffic_hours(bucket,connection_key,app_key,app,target,network,chain,upload,download,first_seen,last_seen) VALUES('2002-01-02T03',?,?,?,'local','tcp','',123,456,1,2)",
                        new Object[]{name, name, name});
                }
                passed = true;
                return;
            }
            bridge = Class.forName("com.smartvpn.client.CoreBridge", true, app.getClassLoader());
            if ("legacy".equals(distributionBackend)) {
                bootstrap = new JSONObject((String)bridge.getMethod("start", String.class, String.class)
                    .invoke(null, app.getFilesDir().getAbsolutePath(), "com/smartvpn/client/CoreBridge"));
            } else {
                bootstrap = new JSONObject(
                    (String) bridge.getMethod("startConfigured", Context.class).invoke(null, app));
            }
            check(bootstrap.has("port"), "bootstrap failed: " + bootstrap.optString("error"));
            if (nodeLink != null) {
                checkNodeLink();
                passed = true;
                return;
            }
            if (distributionBackend != null) {
                boolean rust = "rust".equals(distributionBackend);
                String maps = read(new FileInputStream("/proc/self/maps"));
                for (String name : new String[]{"core", "host", "store"}) {
                    check(maps.contains("libsmartvpn_" + name + ".so") == rust,
                        "unexpected loaded " + name + " for " + distributionBackend);
                }
                check(workerRunning("smartvpn-api") == rust, "unexpected public API backend");
                report("verified distribution backend " + distributionBackend);
            }
            if (requireWriteHost) {check(workerRunning("smartvpn-api"), "integrated Rust API missing"); report("Rust write host active");}
            if (requireReadHost) {
                check(workerRunning("smartvpn-reads"), "service read host missing");
                api("GET", "state", null);
                report("Rust read listener served idle state");
            }
            if (storeStep != null) {
                checkStoreUpgrade();
                passed = true;
                return;
            }
            String uri = "vless://" + node.id + "@127.0.0.1:" + node.server.getLocalPort()
                + "?security=none&type=tcp#" + NAME;

            api("POST", "nodes/manual", new JSONObject().put("name", NAME).put("uri", uri));
            added = true;
            String mergedNode = NAME + "-merged";
            if (testLiveMerge) {
                JSONObject catalog = api("GET", "subscriptions", null);
                check(catalog.has("subscriptions") && (catalog.isNull("subscriptions")
                    || catalog.getJSONArray("subscriptions").length() == 0),
                    "live merge needs an isolated empty subscription catalog");
                String source = uri.substring(0, uri.lastIndexOf('#') + 1) + mergedNode;
                subscriptionId = api("POST", "subscriptions/add", new JSONObject()
                    .put("url", source).put("enabled", true)).getJSONObject("subscription").getString("id");
            }
            check(hasNode("nodes/manual", NAME) && hasNode("nodes", NAME),
                "new manual node missing from the client queries");
            for (boolean revoke : new boolean[] {false, true, false}) {
                check(VpnService.prepare(app) == null, "SmartVPN requires emulator consent");
                File serviceLog = new File(app.getFilesDir(), "SmartVPN/service.log");
                long logStart = serviceLog.length();
                app.startForegroundService(vpn().setAction("com.smartvpn.client.START"));
                awaitTunnel(true);
                api("POST", "connect", null);
                api("PUT", "nodes/select", new JSONObject().put("name", NAME));
                check(api("GET", "identify", null).getBoolean("connected"), "kernel idle");
                check(read(new FileInputStream("/proc/self/maps")).contains("libsmartvpn_host.so"),
                    "Rust host not loaded");
                check(api("GET", "traffic", null).optBoolean("available"), "traffic query failed");
                check(workerRunning("smartvpn-monito"), "Rust monitor worker missing");
                int fd = api("GET", "tun", null).getInt("descriptor");
                boolean rustLease = false;
                try (RandomAccessFile log = new RandomAccessFile(serviceLog, "r")) {
                    log.seek(logStart);
                    String marker = "descriptor " + fd + " handed to kernel (Rust lease=true)";
                    for (String line; (line = log.readLine()) != null;) {
                        if (line.contains(marker)) {
                            rustLease = true;
                            break;
                        }
                    }
                }
                check(rustLease, "missing Rust lease handover for this session");
                String device = android.system.Os.readlink("/proc/self/fd/" + fd);
                check(device.contains("/tun"), "unexpected tunnel fd " + device);
                if (testLiveMerge) {
                    api("POST", "subscriptions/merge", null);
                    api("PUT", "nodes/select", new JSONObject().put("name", mergedNode));
                    check(api("GET", "tun", null).getInt("descriptor") == fd,
                        "hot reload replaced the tunnel descriptor");
                    report("live merge selected subscription node on original descriptor");
                }
                peer(node, "probe");
                report("TUN challenge passed on " + tunnel().getString("interface") + " fd=" + fd);
                String iface = tunnel().getString("interface");
                String marker = "before revoke " + UUID.randomUUID();
                if (revoke) {
                    android.util.Log.i("SmartVPNAccept", marker);
                    peer(node, "revoke");
                } else {
                    api("POST", "disconnect", null);
                    app.stopService(vpn());
                }
                awaitTunnel(false);
                peer(node, "underlay");
                for (File entry : new File("/proc/self/fd").listFiles()) {
                    try {
                        check(!device.equals(android.system.Os.readlink(entry.getPath())),
                            "app still owns " + device);
                    } catch (android.system.ErrnoException e) {
                        if (e.errno != android.system.OsConstants.ENOENT)
                            throw e;
                    }
                }
                java.net.NetworkInterface link = java.net.NetworkInterface.getByName(iface);
                if (link != null) {
                    check(!link.isUp(), "old interface still up");
                    report(iface + " is DOWN after teardown");
                }
                if(requireWriteHost) check(workerRunning("smartvpn-api"),"write host stopped with VPN");
                if (requireReadHost) check(workerRunning("smartvpn-reads"), "read host stopped with VPN");
                check(!workerRunning("smartvpn-monito"), "Rust worker survived");
                JSONObject status = api("GET", "tun", null);
                check(!status.getBoolean("active") && !status.getBoolean("elevated")
                        && status.getInt("descriptor") == 0,
                    "stale tunnel report");
                if (revoke) {
                    String log = shell("logcat -d --pid=" + android.os.Process.myPid()
                        + " -s SmartVPN:I SmartVPNAccept:I");
                    int start = log.lastIndexOf(marker);
                    check(start >= 0 && log.indexOf("the system revoked the tunnel", start) > start,
                        "missing Java onRevoke callback");
                    int close = log.indexOf("closing tunnel, descriptor", start);
                    check(close > start && log.indexOf("closing tunnel, descriptor", close + 1) < 0
                            && log.indexOf("could not close the tunnel", start) < 0,
                        "expected one clean Java teardown");
                }
                report(revoke ? "system revoke disconnected kernel and tunnel"
                              : "ordinary disconnect released monitor and tunnel descriptor");
            }
            passed = true;
        } catch (Throwable e) {
            result.putString("stream", android.util.Log.getStackTraceString(e));
        } finally {
            if (subscriptionId != null) {
                try { api("DELETE", "subscriptions?id=" + subscriptionId, null); }
                catch (Exception e) { result.putString("subscriptionCleanup", e.toString()); passed = false; }
            }
            if (added)
                try {
                    api("POST", "disconnect", null);
                } catch (Exception e) {
                    result.putString("disconnect", e.toString());
                    passed = false;
                }
            if (storeStep == null && app != null)
                app.stopService(vpn());
            if (added)
                try {
                    api("DELETE", "nodes/manual?name=" + NAME, null);
                } catch (Exception e) {
                    result.putString("cleanup", e.toString());
                    passed = false;
                }
            finish(passed ? Activity.RESULT_OK : Activity.RESULT_CANCELED, result);
        }
    }
}

