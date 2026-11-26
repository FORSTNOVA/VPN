package com.smartvpn.acceptance;

import android.app.Instrumentation;
import android.content.Context;
import android.content.Intent;
import android.os.Bundle;
import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.nio.file.*;
import org.json.*;

public class Acceptance extends Instrumentation {
  private static final String PKG = "com.smartvpn.client", BASE = "http://127.0.0.1:38471";
  private String root, bearer;
  private String step;
  private String scenario;
  private void log(String message) {
    Bundle b = new Bundle();
    b.putString("stream", message + "\n");
    sendStatus(1, b);
  }
  @Override
  public void onCreate(Bundle args) {
    super.onCreate(args);
    scenario = args.getString("scenario", "modules");
    start();
  }
  private static void require(boolean v, String text) throws IOException {
    if (!v)
      throw new IOException(text);
  }
  private JSONObject api(String method, String path, JSONObject body) throws Exception {
    String operation = method + " " + path;
    step = operation;
    HttpURLConnection h = (HttpURLConnection) new URL(root + path).openConnection(Proxy.NO_PROXY);
    try {
      h.setConnectTimeout(4000);
      h.setReadTimeout(90000);
      h.setRequestMethod(method);
      h.setRequestProperty("Authorization", bearer);
      // Flutter creates a new HttpClient for every API call and closes it in finally.
      h.setRequestProperty("Connection", "close");
      if (body != null) {
        h.setDoOutput(true);
        h.setRequestProperty("Content-Type", "application/json");
        try (OutputStream out = h.getOutputStream()) {
          out.write(body.toString().getBytes(StandardCharsets.UTF_8));
        }
      }
      int code = h.getResponseCode();
      if (code != 200) {
        String error = new String(h.getErrorStream().readAllBytes(), StandardCharsets.UTF_8);
        throw new IOException("HTTP " + code + " "
            + new JSONObject(error).optString("error").replaceAll("(?i)https?://[^ ]+", "[url]"));
      }
      log(operation + " HTTP " + code);
      try (InputStream response = h.getInputStream()) {
        return new JSONObject(new String(response.readAllBytes(), StandardCharsets.UTF_8));
      }
    } catch (IOException error) {
      throw new IOException(operation + ": " + error.getMessage(), error);
    } finally {
      h.disconnect();
    }
  }
  private JSONObject post(String path, JSONObject body) throws Exception {
    return api("POST", path, body);
  }
  private JSONObject get(String path) throws Exception {
    return api("GET", path, null);
  }
  private static JSONObject obj(Object... pairs) throws Exception {
    JSONObject o = new JSONObject();
    for (int i = 0; i < pairs.length; i += 2) o.put((String) pairs[i], pairs[i + 1]);
    return o;
  }
  private void authorizeTunnel(Context ctx) throws Exception {
    Intent activity = ctx.getPackageManager().getLaunchIntentForPackage(PKG);
    ctx.startActivity(activity.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK));
    Thread.sleep(3500);
    require(android.net.VpnService.prepare(ctx) == null, "VPN consent required");
    Intent service =
        new Intent().setClassName(PKG, PKG + ".SmartVpnService").setAction(PKG + ".START");
    ctx.startForegroundService(service);
    for (int i = 0; i < 50 && !get("status").getBoolean("tunnelAuthorized"); i++) Thread.sleep(200);
    require(get("status").getBoolean("tunnelAuthorized"), "VPN authorization");
  }
  private void batch(boolean connected) throws Exception {
    java.util.concurrent.ExecutorService pool = java.util.concurrent.Executors.newFixedThreadPool(4);
    java.util.List<java.util.concurrent.Future<JSONObject>> pending = new java.util.ArrayList<>();
    long start = android.os.SystemClock.elapsedRealtime();
    try {
      for (int i=0;i<4;i++) {
        final String name="fixture-"+i;
        pending.add(pool.submit(() -> post("nodes/chain/probe",obj("hops",new JSONArray().put(name)))));
      }
      require(get("status").getBoolean("connected")==connected,"unexpected tunnel state");
      for (int i=0;i<4;i++) {
        JSONObject r;
        try { r=pending.get(i).get(15,java.util.concurrent.TimeUnit.SECONDS); }
        catch (java.util.concurrent.TimeoutException timeout) {
          throw new IOException("probe "+i+" exceeded 15 seconds, connected="+connected,timeout);
        }
        require(!r.has("error"),"probe "+i+" "+r.optString("error"));
        int delay=r.getInt("delay"),warm=r.getInt("warmDelay");
        require(delay>0 && warm>0,"missing timing");
        log("batch connected="+connected+" node="+i+" delay="+delay+" warm="+warm);
      }
      log("batch elapsedMs="+(android.os.SystemClock.elapsedRealtime()-start));
    } finally { pool.shutdownNow(); }
  }
  private void runChecks() throws Exception {
    Context ctx = getTargetContext();
    Path dir = ctx.getFilesDir().toPath();
    Files.write(dir.resolve("china-mirrors.txt"), (BASE + "/").getBytes(StandardCharsets.UTF_8));
    Files.write(dir.resolve("latency-target.txt"),
        "http://198.51.100.7/probe".getBytes(StandardCharsets.UTF_8));
    Files.write(dir.resolve("egress-target.txt"),
        "http://198.51.100.7/egress".getBytes(StandardCharsets.UTF_8));
    Files.write(
        dir.resolve("speed-origin.txt"), "http://198.51.100.7".getBytes(StandardCharsets.UTF_8));
    Files.write(dir.resolve("region-trace.txt"),
        "http://198.51.100.7/trace".getBytes(StandardCharsets.UTF_8));
    Files.write(dir.resolve("region-country.txt"),
        "http://198.51.100.7/country".getBytes(StandardCharsets.UTF_8));
    Class<?> bridge = ctx.getClassLoader().loadClass(PKG + ".CoreBridge");
    JSONObject bootstrap = new JSONObject(
        (String) bridge.getMethod("startConfigured", Context.class).invoke(null, ctx));
    root = "http://127.0.0.1:" + bootstrap.getInt("port") + "/api/";
    bearer = "Bearer " + bootstrap.getString("token");
    get("state");
    if (scenario.equals("recovery")) {
      String id = post("subscriptions/add", obj("url",BASE+"/base64","label","fixture")).getString("id");
      post("subscriptions/fetch",obj("id",id));
      JSONObject quota=get("subscriptions").getJSONArray("subscriptions").getJSONObject(0).getJSONObject("userInfo");
      require(quota.getLong("total")==1024 && quota.getLong("expire")==1900000000 && quota.getLong("upload")+quota.getLong("download")==3,"immediate quota");
      log("Fetch persisted quota");
      authorizeTunnel(ctx);
      try {
        post("connect",null);
        JSONObject before=get("status");
        int fd=before.getInt("tunnelFd");
        for (int pass=0;pass<2;pass++) {
          try (Socket stale=new Socket("127.0.0.1",before.getInt("mixedPort"))) {
            stale.setSoTimeout(6000);
            stale.getOutputStream().write("CONNECT 198.51.100.7:80 HTTP/1.1\r\nHost: 198.51.100.7:80\r\n\r\n".getBytes(StandardCharsets.US_ASCII));
            BufferedReader in=new BufferedReader(new InputStreamReader(stale.getInputStream(),StandardCharsets.US_ASCII));
            require(in.readLine().contains("200"),"establish stale flow");
            while (!in.readLine().isEmpty()) {}
            stale.getOutputStream().write("GET /recovery-hold HTTP/1.1\r\nHost: fixture\r\n\r\n".getBytes(StandardCharsets.US_ASCII));
            require("HTTP/1.1 200 OK".equals(in.readLine()),"hold origin handshake");
            while (!in.readLine().isEmpty()) {}
            require(get("connections").getJSONArray("connections").length()>0,"missing tracked flow");
            android.net.ConnectivityManager cm=ctx.getSystemService(android.net.ConnectivityManager.class);
            android.net.NetworkCapabilities caps=cm.getNetworkCapabilities(cm.getActiveNetwork());
            log("Underlying Wi-Fi="+caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_WIFI)+" cellular="+caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_CELLULAR));
            log("READY FOR UPLINK SWITCH");
            stale.setSoTimeout(25000);
            require(in.read()==-1,"old connection still alive");
            android.util.Log.i("SmartVPN", "Held fixture socket closed");
            android.net.NetworkCapabilities next=cm.getNetworkCapabilities(cm.getActiveNetwork());
            int expected=pass==0 ? android.net.NetworkCapabilities.TRANSPORT_CELLULAR : android.net.NetworkCapabilities.TRANSPORT_WIFI;
            require(next!=null && next.hasTransport(expected),"expected transport did not become default");
          }
          JSONObject after=get("status");
          require(after.getBoolean("connected") && after.getInt("tunnelFd")==fd && after.getInt("controllerPort")==before.getInt("controllerPort"),"tunnel replaced");
          log("STALE FLOW CLOSED SAME TUNNEL");
          // Host runs a separate shell UID through the TUN and acknowledges success.
          long deadline=android.os.SystemClock.elapsedRealtime()+20000;
          while (((HttpURLConnection)new URL(BASE+"/test/ui-finished").openConnection(Proxy.NO_PROXY)).getResponseCode()!=200) {
            require(android.os.SystemClock.elapsedRealtime()<deadline,"waiting for post-switch TUN probe");
            Thread.sleep(100);
          }
        }
      } finally { post("disconnect",null); }
      log("RECOVERY PASSED");
      return;
    }
    if (scenario.equals("https")) {
      int reachable=0;
      // Proxy.NO_PROXY bypasses the guest HTTP proxy. Host routing still applies.
      for (String host : new String[]{"www.youtube.com","www.gstatic.com"}) {
        try (ServerSocket listener = new ServerSocket(0, 4, InetAddress.getByName("127.0.0.1"))) {
          listener.setSoTimeout(6000);
          java.util.concurrent.ExecutorService worker=java.util.concurrent.Executors.newSingleThreadExecutor();
          try {
            java.util.concurrent.Future<?> relay=worker.submit(() -> {
              try (Socket client=listener.accept(); Socket upstream=new Socket(Proxy.NO_PROXY)) {
                client.setSoTimeout(6000);
                DataInputStream in=new DataInputStream(client.getInputStream());
                OutputStream out=client.getOutputStream();
                require(in.readUnsignedByte()==5,"SOCKS version");
                in.readNBytes(in.readUnsignedByte());
                out.write(new byte[]{5,0});
                require(in.readInt()==0x05010003,"expected SOCKS domain CONNECT");
                require(host.equals(new String(in.readNBytes(in.readUnsignedByte()),StandardCharsets.US_ASCII))
                    && in.readUnsignedShort()==443,"unexpected TLS destination");
                log(host+" SOCKS request received");
                upstream.connect(new InetSocketAddress(host,443),4000);
                log(host+" upstream TCP connected");
                upstream.setSoTimeout(6000);
                out.write(new byte[]{5,0,0,1,127,0,0,1,0,0});
                Thread upload=new Thread(() -> {
                  try { in.transferTo(upstream.getOutputStream()); }
                  catch (IOException e) { if (!client.isClosed()) log("TLS client stream ended: "+e.getClass().getSimpleName()); }
                });
                upload.start();
                upstream.getInputStream().transferTo(out);
              }
              return null;
            });
            String name="public-"+host;
            post("nodes/manual",obj("uri","socks5://127.0.0.1:"+listener.getLocalPort(),"name",name));
            JSONObject row=post("nodes/chain/probe",obj("hops",new JSONArray().put(name),"url","https://"+host+"/generate_204"));
            log(host+" "+(row.has("error") ? row.getString("error") : "delay="+row.getInt("delay")+" warm="+row.getInt("warmDelay")));
            if (!row.has("error")) reachable++;
            try { relay.get(7,java.util.concurrent.TimeUnit.SECONDS); }
            catch (java.util.concurrent.ExecutionException err) { log("guest relay: "+err.getCause().getClass().getSimpleName()); }
          } finally { worker.shutdownNow(); }
        }
      }
      require(reachable==2,"public HTTPS targets reachable: "+reachable+"/2");
      return;
    }
    if (scenario.equals("dead")) {
      Files.writeString(dir.resolve("latency-target.txt"),"http://timeout.example/probe");
      String id=post("subscriptions/add",obj("url","http://127.0.0.1:38571/dead","label","timeouts")).getString("id");
      require(post("subscriptions/fetch",obj("id",id)).getInt("nodes")==34,"timeout catalog size");
      authorizeTunnel(ctx);
      try {
        long start=android.os.SystemClock.elapsedRealtime();
        JSONObject result=post("connect",null);
        long elapsed=android.os.SystemClock.elapsedRealtime()-start;
        require(result.getBoolean("connected") && !result.getBoolean("autoSwitched") &&
            elapsed>=4900 && elapsed<6500,"timeout budget, elapsedMs="+elapsed);
        log("allTimedOutConnectMs="+elapsed);
      } finally { post("disconnect",null); }
      return;
    }
    if (scenario.equals("rules")) {
      api("PUT","settings",obj("domesticWeb",true,"webRTCProtection",true,"dnsProtection",true,"importPolicy",true));
      String id=post("subscriptions/add",obj("url",BASE+"/rule-policy","label","rule fixture")).getString("id");
      require(post("subscriptions/fetch",obj("id",id)).getInt("nodes")==1,"fixture import");
      require(get("subscription/policy").getJSONArray("rules").length()==6,"normalized rule count");
      authorizeTunnel(ctx);
      try {
        require(post("connect",null).getBoolean("connected"),"connect");
        JSONObject tun=get("tun");
        require(tun.getBoolean("active") && tun.getJSONObject("policy").getJSONArray("quicRules").length()==1,"TUN/QUIC");
        byte[] digest=java.security.MessageDigest.getInstance("SHA-256").digest(Files.readAllBytes(dir.resolve("Country.mmdb")));
        require(new java.math.BigInteger(1,digest).toString(16).equals("9840946762fa8c2673738bbbbab22bb720bde931f6dea327eb58e689babc37dc"),"country database");
        Path caches=dir.resolve("providers/sub-rules");
        boolean loaded=false;
        for (int i=0;i<50 && !loaded;i++) {
          try (var paths=Files.list(caches)) {
            java.util.Set<String> payloads=new java.util.HashSet<>();
            for (Path path:paths.toArray(Path[]::new)) payloads.add(Files.readString(path));
            loaded=payloads.equals(java.util.Set.of("blocked.example\n","payload:\n  - 198.51.100.99/32\n"));
          }
          if (!loaded) Thread.sleep(100);
        }
        require(loaded,"provider cache initialization");
        log("RULE TUN READY");
        boolean done=false;
        for (int i=0;i<90 && !done;i++) {
          HttpURLConnection sync=(HttpURLConnection)new URL(BASE+"/test/ui-finished").openConnection(Proxy.NO_PROXY);
          try { done=sync.getResponseCode()==200; } finally { sync.disconnect(); }
          if (!done) Thread.sleep(500);
        }
        require(done,"external TUN probe timeout");
      } finally { post("disconnect",null); }
      JSONObject status=get("status");
      require(!status.getBoolean("connected") && status.isNull("tunnelFd"),"disconnect");
      log("RULE ACCEPTANCE PASSED");
      return;
    }
    if (scenario.equals("dns")) {
      Files.createDirectories(dir.resolve("providers"));
      Files.writeString(dir.resolve("providers/china-ip.txt"), "192.0.2.0/24\n");
      api("PUT","settings",obj("domesticWeb",true,"dnsProtection",true));
      Files.writeString(dir.resolve("latency-target.txt"), "https://www.gstatic.com/generate_204");
      Files.delete(dir.resolve("egress-target.txt")); // Use the production HTTPS egress test.
      String id=post("subscriptions/add",obj("url","http://127.0.0.1:38571/dns","label","DNS")).getString("id");
      require(post("subscriptions/fetch",obj("id",id)).getInt("nodes")==34,"subscription size");
      post("nodes/select",obj("name","primary"));
      authorizeTunnel(ctx);
      try {
        long start=android.os.SystemClock.elapsedRealtime();
        JSONObject connected=post("connect",null);
        long elapsed=android.os.SystemClock.elapsedRealtime()-start;
        log("connectMs="+elapsed);
        require(elapsed<6500,"connect exceeded bounded probe budget");
        require(connected.getBoolean("connected") && !connected.getBoolean("autoSwitched"),"connect state");
        JSONObject tun=post("tun/check",null);
        require(tun.getBoolean("active") && tun.getJSONObject("policy").getString("dnsMode").equals("fake-ip")
            && tun.getJSONArray("checks").getJSONObject(0).getString("state").equals("ok"),"tun/check");
        log("TUN READY");
        log("latencyMs="+get("latency").getInt("delay"));
        for (JSONArray hops : new JSONArray[]{new JSONArray().put("primary"),new JSONArray().put("primary").put("exit")}) {
          JSONObject row=post("nodes/chain/probe",obj("hops",hops));
          require(!row.has("error"),"chain probe: "+row.optString("error"));
          log("hops="+hops.length()+" delay="+row.getInt("delay")+" warm="+row.getInt("warmDelay"));
        }
        JSONObject diagnostics=post("diagnose",null);
        require(diagnostics.getJSONObject("egress").has("ipv4")
            && diagnostics.getJSONArray("sites").getJSONObject(0).getInt("httpCode")==204,"diagnose");
        log("diagnose HTTPS 204");
        Thread.sleep(12000);
      } finally { post("disconnect",null); }
      post("nodes/select",obj("name","dead-0"));
      authorizeTunnel(ctx);
      try {
        long start=android.os.SystemClock.elapsedRealtime();
        JSONObject recovered=post("connect",null);
        long elapsed=android.os.SystemClock.elapsedRealtime()-start;
        require(recovered.getBoolean("connected") && recovered.getBoolean("autoSwitched")
            && elapsed<6500,"bounded recovery");
        log("deadSelectionRecoveryMs="+elapsed);
      } finally { post("disconnect",null); }
      return;
    }
    if (scenario.equals("ipv6-lan")) {
      post("nodes/manual",obj("uri","socks5://127.0.0.1:38472","name","fixture"));
      api("PUT","nodes/select",obj("name","fixture"));
      authorizeTunnel(ctx);
      post("connect",null);
      try (ServerSocket origin=new ServerSocket(0,1,InetAddress.getByName("::1"));
           Socket mixed=new Socket("127.0.0.1",get("status").getInt("mixedPort"))) {
        origin.setSoTimeout(5000);mixed.setSoTimeout(5000);
        String target="[::1]:"+origin.getLocalPort();
        mixed.getOutputStream().write(("CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n").getBytes(StandardCharsets.US_ASCII));
        BufferedReader response=new BufferedReader(new InputStreamReader(mixed.getInputStream(),StandardCharsets.US_ASCII));
        require(response.readLine().contains("200"),"IPv6 CONNECT status");
        while(!response.readLine().isEmpty()) {}
        byte[] payload="ipv6-lan-direct".getBytes(StandardCharsets.US_ASCII);
        mixed.getOutputStream().write(payload);
        log("IPv6 CONNECT accepted, awaiting origin");
        try(Socket accepted=origin.accept()) {
          accepted.setSoTimeout(5000);
          require(java.util.Arrays.equals(payload,accepted.getInputStream().readNBytes(payload.length)),"IPv6 payload");
          boolean direct=false;
          JSONArray flows=get("traffic").getJSONArray("connections");
          for(int i=0;i<flows.length();i++) {
            JSONObject row=flows.getJSONObject(i);
            if (row.getString("destinationPort").equals(Integer.toString(origin.getLocalPort())))
              direct=row.getString("chain").contains("DIRECT");
          }
          require(direct,"IPv6 direct route");
          log("IPV6 LAN DONE actual bytes="+payload.length+" route=DIRECT");
        }
      } finally {post("disconnect",null);}
      return;
    }
    if (scenario.equals("api-contracts")) {
      require(get("identify").getString("kernel").equals("clash-rs"),"kernel identity");
      String id=post("subscriptions/add",obj("url",BASE+"/base64","label","before","prefix","")).getString("id");
      api("PUT","subscriptions",obj("id",id,"label","renamed"));
      require(get("subscriptions").getJSONArray("subscriptions").getJSONObject(0).getString("label").equals("renamed"),"rename did not persist");
      require(post("subscriptions/activate",obj("id",id)).getInt("nodes")==1,"activate catalog");
      String name=post("nodes/refresh",obj("id",id)).getJSONArray("nodes").getJSONObject(0).getString("name");
      api("PUT","nodes/select",obj("name",name));
      require(get("state").getString("selectedNode").equals(name),"selection not persisted");
      post("nodes/manual",obj("name","manual-fixture","uri","socks5://127.0.0.1:38472"));
      JSONObject item=get("nodes/manual").getJSONArray("nodes").getJSONObject(0);
      require(item.getInt("port")==38472 && item.getLong("addedAt")>0
          && !item.has("uri") && item.getString("name").equals("manual-fixture"),"manual node descriptor");
      api("DELETE","nodes/manual?name=manual-fixture",null);
      require(get("nodes/manual").getJSONArray("nodes").length()==0,"deleted manual node retained");
      require(post("shutdown",null).getBoolean("ok"),"shutdown acknowledgement");
      log("API CONTRACTS DONE");
      return;
    }
    if (scenario.equals("slow-handshake")) {
      post("nodes/manual",obj("uri","socks5://127.0.0.1:38573","name","timing-control"));
      JSONObject sample=post("nodes/chain/probe",obj("hops",new JSONArray().put("timing-control")));
      long overall=sample.getLong("delay"),http=sample.getLong("warmDelay");
      require(overall-http>=1250 && http>=20 && http<1000,"controlled handshake timing mismatch");
      log("DELAY CONTROL DONE overall="+overall+" http="+http);
      return;
    }
    if (scenario.equals("peers")) {
      String id=post("subscriptions/add",obj("url","http://127.0.0.1:38690/peers","label","schema")).getString("id");
      JSONObject imported=post("subscriptions/fetch",obj("id",id));
      require(imported.getInt("skipped")==0,"kernel schema skipped entries: "+imported.getInt("skipped"));
      JSONArray nodes=get("nodes").getJSONArray("nodes");
      int passed=0;
      for(int i=0;i<nodes.length();i++) {
        String name=nodes.getJSONObject(i).getString("name");
        JSONObject sample=post("nodes/chain/probe",obj("hops",new JSONArray().put(name)));
        if(sample.has("error")) log("PEER FAIL "+name+" "+sample.getString("error"));
        else {
          passed++;
          log("PEER OK "+name+" overall="+sample.getLong("delay")+" http="+sample.getLong("warmDelay"));
        }
      }
      require(passed==nodes.length() && passed>0,"peer handshakes "+passed+"/"+nodes.length());
      log("PEERS PASSED");
      return;
    }
    if (scenario.equals("batch")) {
      String id=post("subscriptions/add",obj("url","http://127.0.0.1:38571/sub","label","fixture")).getString("id");
      JSONObject imported=post("subscriptions/fetch",obj("id",id));
      require(imported.getInt("nodes")==6 && imported.getInt("skipped")==1,"mixed subscription count");
      post("nodes/select",obj("name","fixture-0"));
      batch(false);
      authorizeTunnel(ctx);
      try {
        post("connect",null);
        require(get("tun").getBoolean("active"),"TUN inactive");
        batch(true);
        JSONObject chain=post("nodes/chain/probe",obj("hops",new JSONArray().put("fixture-0").put("fixture-1")));
        require(!chain.has("error"),"running tunnel chain probe "+chain.optString("error"));
        log("connected ad-hoc delay="+chain.getInt("delay")+" warm="+chain.getInt("warmDelay"));
        require(get("tun").getBoolean("active"),"TUN lost");
      } finally { post("disconnect",null); }
      require(get("status").isNull("tunnelFd"),"TUN fd retained");
      log("BATCH PASSED");
      return;
    }
    if (scenario.equals("matrix-seed")) {
      for(String[] src:new String[][]{{"redirect","YAML-"},{"base64","Base64-"}}) {
        post("subscriptions/add",obj("url",BASE+"/"+src[0],"label",src[0],"prefix",src[1]));
      }
      require(post("subscriptions/merge",null).getInt("nodes")==2,"matrix catalog");
      log("MATRIX SEED DONE");
      return;
    }
    if (scenario.equals("matrix")) {
      JSONArray hops = new JSONArray().put("YAML-First").put("Base64-Second");
      api("PUT","nodes/chain",obj("name","matrix","enabled",true,"hops",hops));
      for (int mask=0; mask<16; mask++) {
        boolean domestic=(mask&1)!=0, webRtc=(mask&2)!=0, dns=(mask&4)!=0, locked=(mask&8)!=0;
        api("PUT","settings",obj("domesticWeb",domestic,"webRTCProtection",webRtc,"dnsProtection",dns));
        post("region/lock",obj("country",locked?"US":""));
        authorizeTunnel(ctx);
        post("connect",null);
        JSONObject state=get("state"), effective=get("tun").getJSONObject("policy");
        require(state.getBoolean("connected") && state.getString("activeNode").equals("Base64-Second") &&
            state.getString("lockedRegion").equals(locked?"US":"") &&
            state.getBoolean("domesticWeb")==domestic && state.getBoolean("webRTCProtection")==webRtc &&
            state.getBoolean("dnsProtection")==dns, "matrix settings "+mask);
        require((effective.getJSONArray("quicRules").length()>0)==(!webRtc||domestic), "matrix QUIC "+mask);
        int delay=get("latency").getInt("delay");
        require(get("traffic").getBoolean("available") && delay>=0,"matrix traffic "+mask);
        post("disconnect",null);
        require(get("status").isNull("tunnelFd"),"matrix fd "+mask);
        log("MATRIX "+mask+" domestic="+domestic+" webrtc="+webRtc+" dns="+dns+" lock="+locked+" delay="+delay);
      }
      api("PUT","nodes/chain",obj("enabled",false,"hops",new JSONArray()));
      post("region/lock",obj("country",""));
      log("MATRIX DONE " + get("status").getJSONObject("httpResponses"));
      return;
    }
    if (scenario.equals("history")) {
      for (String period : new String[]{"hour","day","month","year"}) {
        JSONObject history=get("traffic/history?group=app&period="+period);
        require(history.getLong("uploadTotal")>=49152,"persisted "+period);
        String app=history.getJSONArray("rows").getJSONObject(0).getString("app");
        require(get("traffic/history?period="+period+"&app="+URLEncoder.encode(app,"UTF-8")).getInt("rowCount")>0,"app filter");
      }
      log("PERSISTED HISTORY DONE");
      return;
    }
    if (scenario.equals("chain-seed")) {
      for (String source : new String[]{"chain-entry","chain-exit"})
        post("subscriptions/add",obj("url",BASE+"/"+source,"label",source,"prefix",""));
      require(post("subscriptions/merge",null).getInt("nodes")==3,"chain seed catalog");
      post("nodes/select",obj("name","中继 [B]"));
      api("PUT","nodes/chain",obj("enabled",true,"hops",new JSONArray().put("入口 [A]").put("出口 [C]")));
      log("CHAIN SEED DONE");
      return;
    }
    if (scenario.equals("chain-reload")) {
      require(get("state").getString("selectedNode").equals("出口 [C]"),"persisted chain exit");
      authorizeTunnel(ctx);
      try {
        post("connect",null);
        require(get("state").getString("activeNode").equals("出口 [C]"),"reloaded active node");
        log("TWO HOP READY");
        boolean observed=false;
        for (int attempt=0;attempt<80 && !observed;attempt++) {
          JSONArray flows=get("traffic").getJSONArray("connections");
          for(int i=0;i<flows.length();i++){
            JSONObject row=flows.getJSONObject(i);
            if(row.getString("target").endsWith(":38476")){
              require(row.getString("chain").equals("入口 [A] → 出口 [C] → SmartVPN")
                  && row.getString("process").equals("com.android.shell"),"wrong reloaded flow");
              observed=true;
            }
          }
          if(!observed) Thread.sleep(100);
        }
        require(observed,"missing reloaded flow");
        Thread.sleep(2500);
      } finally {post("disconnect",null);}
      api("PUT","nodes/chain",obj("enabled",false,"hops",new JSONArray()));
      require(get("state").getString("selectedNode").equals("中继 [B]"),"lost prior single selection");
      log("CHAIN RELOAD DONE");
      return;
    }
    if (scenario.equals("chain")) {
      String entry = "入口 [A]", middle = "中继 [B]", exit = "出口 [C]";
      for (String source : new String[]{"chain-exit", "chain-entry"}) {
        post("subscriptions/add", obj("url", BASE + "/" + source,"label",source,"prefix",""));
      }
      require(post("subscriptions/merge",null).getInt("nodes")==3,"merge chain catalog");
      log("chain catalog " + get("nodes").getJSONArray("nodes"));
      JSONArray hops = new JSONArray().put(entry).put(middle).put(exit);
      JSONObject two = post("nodes/chain/probe",obj("hops",new JSONArray().put(entry).put(exit)));
      require(two.getInt("delay") >= 0, "two hop " + two);
      log("reverse-order 2-hop " + two);
      JSONObject prefix = post("nodes/chain/probe",obj("hops",new JSONArray().put(entry).put(middle)));
      require(prefix.getInt("delay")>=0 && prefix.getInt("warmDelay")>=0,"prefix timing");
      log("3-hop prefix " + prefix);
      JSONObject three = post("nodes/chain/probe", obj("hops",hops));
      require(!three.has("error"), "three hop " + three);
      log("reverse-order 3-hop " + three);
      api("PUT","settings",obj("domesticWeb",true,"webRTCProtection",true,"dnsProtection",true));
      api("PUT","nodes/chain/profile",obj("original","","name","chain","hops",hops));
      api("PUT","nodes/chain/profile",obj("original","chain","name","three","hops",hops));
      post("nodes/select",obj("name",entry));
      api("PUT","nodes/chain",obj("name","three","enabled",true,"hops",hops));
      require(get("state").getString("selectedNode").equals(exit),"disconnected chain exit");
      api("PUT","path-checks",obj("enabled",false,"intervalSec",120,"targets",
          new JSONArray().put(obj("enabled",true,"label","local","url","http://198.51.100.7/path"))));
      post("region/lock",obj("country","US"));
      api("PUT","settings",obj("appGroups",new JSONArray().put(obj("id","games","name","Games","targetType","direct","enabled",true,"apps",new JSONArray().put("com.android.settings").put("com.android.chrome")))));
      authorizeTunnel(ctx);
      post("connect",null);
      require(get("state").getString("activeNode").equals(exit),"active exit");
      require(get("latency").getInt("delay") >= 0,"active latency");
      JSONArray results=get("nodes/latency").getJSONArray("results");
      require(results.length()==1 && results.getJSONObject(0).getString("name").equals(exit)
              && results.getJSONObject(0).getInt("latencyMs") >= 0,"chain group latency");
      post("health/patrol", null);
      require(post("path-checks/run",null).getJSONObject("status").getJSONArray("failures").length()==0,"chain path failed");
      post("disconnect",null);
      authorizeTunnel(ctx);
      post("connect",null);
      JSONObject resumed = get("state");
      require(resumed.getBoolean("connected") && resumed.getString("activeNode").equals(exit)
          && resumed.getString("selectedNode").equals(exit),"chain changed after reconnect");
      log("CHAIN CONNECTED WINDOW");
      boolean sawFlow = false;
      for (int n=0;n<80 && !sawFlow;n++) {
        JSONArray flows=get("traffic").getJSONArray("connections");
        for(int f=0;f<flows.length();f++){
          JSONObject flow=flows.getJSONObject(f);
          if(flow.getString("target").contains("38476")){
            log("chain traffic " + flow);
            require(flow.getString("chain").equals(entry + " → " + middle + " → " + exit + " → SmartVPN"), "missing outbound hops");
            require(!flow.isNull("process") && !flow.getString("process").isEmpty(), "missing Android socket owner");
            sawFlow=true;
            break;
          }
        }
        Thread.sleep(100);
      }
      require(sawFlow,"live chain flow missing");
      Thread.sleep(2500);
      post("disconnect",null);
      require(get("status").isNull("tunnelFd"),"chain fd retained");
      api("PUT","nodes/chain",obj("enabled",false,"hops",new JSONArray()));
      post("region/lock",obj("country",""));
      post("nodes/select",obj("name",exit));
      authorizeTunnel(ctx);
      post("connect",null);
      require(get("state").getString("activeNode").equals(exit),"ordinary connection after chain");
      post("disconnect",null);
      log("chain HTTP counts " + get("status").getJSONObject("httpResponses"));
      log("CHAIN PROBES DONE");
      return;
    }
    HttpURLConnection reset =
        (HttpURLConnection) new URL(BASE + "/test/reset").openConnection(Proxy.NO_PROXY);
    reset.setRequestMethod("POST");
    require(reset.getResponseCode() == 204, "fixture reset");
    reset.disconnect();
    for (String[] src : new String[][] {{"redirect", "YAML"}, {"base64", "Base64"}}) {
      String id = post(
          "subscriptions/add", obj("url", BASE + "/" + src[0], "label", src[1], "enabled", true))
                      .getString("id");
      post("subscriptions/update", obj("id", id, "prefix", src[1] + "-"));
      require(post("subscriptions/fetch", obj("id", id)).getInt("nodes") == 1, "fetch count");
      require(post("subscriptions/refresh", obj("id", id))
                  .getJSONObject("subscription")
                  .getInt("nodeCount")
              == 1,
          "refresh count");
      require(post("subscriptions/quota", obj("id", id)).getJSONObject("userInfo").getInt("total")
              == 1024,
          "quota");
    }
    require(post("subscriptions/merge", null).getInt("nodes") == 2, "merge count");
    require(
        get("nodes").getJSONArray("nodes").getJSONObject(0).getString("sourceLabel").equals("YAML"),
        "node group");
    post("subscriptions/auto-merge", obj("hours", 1));
    require(get("diagnostics/subscription").getJSONObject("cache").getInt("selectableNodes")==2,"diagnostic catalog");
    String disposable=post("subscriptions/add",obj("url",BASE+"/unused","label","unused")).getString("id");
    api("DELETE","subscriptions?id="+disposable,null);
    for(String uri:new String[]{"trojan://test@127.0.0.1:38472","socks5://127.0.0.1:38472"}) {
      post("nodes/manual",obj("uri",uri,"name","temporary"));
      api("DELETE","nodes/manual?name=temporary",null);
    }
    post("nodes/select",obj("name","Base64-Second"));
    for(String key:new String[]{"dnsProtection","domesticWeb","webRTCProtection"}){
      api("PUT","settings",obj(key,true));
      require(get("settings").getBoolean(key),"save "+key);
      api("PUT","settings",obj(key,false));
      require(!get("settings").getBoolean(key),"clear "+key);
    }
    post("direct/refresh", null);
    JSONObject china;
    do {
      Thread.sleep(150);
      china = get("state").getJSONObject("chinaDirect");
    } while (china.getBoolean("refreshing"));
    require(china.getInt("entries") == 2 && china.getString("error").isEmpty(),
        "CIDR cache " + china.optString("error"));
    require(Files.readAllLines(dir.resolve("providers/china-ip.txt")).size() == 2, "CIDR file");
    for (JSONArray hops : new JSONArray[] {new JSONArray("[\"YAML-First\"]"),
             new JSONArray("[\"YAML-First\",\"Base64-Second\"]")}) {
      JSONObject r = post("nodes/chain/probe", obj("hops", hops));
      require(!r.has("error"), "probe: " + r.optString("error"));
      log("hops=" + hops.length() + " cold=" + r.getInt("delay") + " warm=" + r.getInt("warmDelay")
          + " ms");
    }
    require(get("nodes/probes").getJSONArray("results").length() >= 2, "saved probes");
    api("PUT", "settings", obj("domesticWeb", true, "webRTCProtection", true));
    api("PUT", "path-checks",
        obj("enabled", false, "intervalSec", 120, "targets",
            new JSONArray().put(
                obj("enabled", true, "label", "Fixture", "url", "http://198.51.100.7/path"))));
    get("events");
    get("health/params");
    authorizeTunnel(ctx);
    post("connect", null);
    require(
        get("state").getString("activeNode").equals("Base64-Second"), "faster proxy not selected");
    log("faster proxy selected after comparison");
    int quotaFd=get("status").getInt("tunnelFd");
    require(post("subscriptions/quota",
                obj("id",
                    get("subscriptions")
                        .getJSONArray("subscriptions")
                        .getJSONObject(0)
                        .getString("id")))
                .getJSONObject("userInfo")
                .getInt("total")
            == 1024,
        "connected quota");
    require(get("status").getInt("tunnelFd")==quotaFd && get("state").getString("activeNode").equals("Base64-Second"),"quota interrupted connection");
    log("quota=1024 same TUN fd="+quotaFd);
    api("PUT", "settings",
        obj("subscriptionUrl", "__merged__", "connectionMode", "vpn", "wintunPath", "",
            "mihomoPath", ""));
    require(post("nodes/fetch", null).getJSONArray("nodes").length() == 2, "connected fetch");
    post("nodes/select",obj("name","YAML-First"));
    require(get("state").getString("activeNode").equals("YAML-First"),"hot select");
    post("nodes/select",obj("name","Base64-Second"));
    get("regions");
    JSONObject timing = get("latency");
    log("active HTTP=" + timing.getInt("delay") + " overall=" + timing.getInt("overall") + " ms");
    JSONArray all =
        get("nodes/latency?url=http%3A%2F%2F198.51.100.7%2Fprobe").getJSONArray("results");
    require(all.length() == 2 && all.getJSONObject(0).getInt("latencyMs") > 0
            && all.getJSONObject(1).getInt("latencyMs") > 0,
        "all delays");
    JSONObject tun = get("tun"), policy = tun.getJSONObject("policy");
    require(tun.getBoolean("active") && !policy.getBoolean("dnsIPv6")
            && policy.getBoolean("dnsHijack") && policy.getInt("mtu") == 1500
            && policy.getJSONArray("quicRules").length() > 0,
        "runtime TUN policy");
    log("policy mtu=1500 IPv6=false dnsHijack=true " + policy.getJSONArray("quicRules"));
    require(tun.getString("interface").equals("tun0"), "unexpected interface");
    log("TUN interface=tun0");
    require(post("tun/check", null).getBoolean("active"), "TUN check");
    require(post("path-checks/run", null).getJSONObject("status").getJSONArray("failures").length()
            == 0,
        "path failed");
    get("path-checks");
    post("path-checks/recovery/cancel", null);
    require(
        post("regions/verify", obj("refresh", true)).getJSONObject("job").getInt("verified") == 2,
        "region cross check");
    require(post("region/lock", obj("country", "US")).getJSONArray("pool").length() == 2,
        "region pool");
    post("region/lock", obj("country", ""));
    post("health/patrol", null);
    JSONObject previousHealth=get("health/params");
    JSONObject changed=new JSONObject(previousHealth.toString()).put("connectTimeoutMs",4100);
    api("PUT","health/params",changed);
    require(get("health/params").getInt("connectTimeoutMs")==4100,"health persistence");
    api("PUT","health/params",previousHealth);
    require(get("events").getJSONArray("events").getJSONObject(0).getString("toNode").equals("Base64-Second"),"switch event");
    post("health/params", get("health/params"));
    get("subscription/policy");
    JSONObject diag = post("diagnose", null);
    require(diag.getJSONObject("egress").getString("ipv4").equals("192.0.2.99")
            && diag.getJSONArray("sites").getJSONObject(0).getInt("httpCode") == 200,
        "diagnose");
    JSONObject speed = post("speedtest", obj("megaBytes", 10)).getJSONObject("speed");
    require(speed.getLong("downBytes") == 10000000 && speed.getLong("upBytes") == 3333333,
        "speed bytes");
    log("speed downBytes=" + speed.getLong("downBytes") + " upBytes=" + speed.getLong("upBytes"));
    log("CONNECTED WINDOW");
    Thread.sleep(30000);
    JSONObject traffic = get("traffic");
    require(traffic.getBoolean("available") && traffic.getLong("downloadTotal") > 0
            && traffic.getLong("uploadTotal") > 0,
        "traffic counters");
    JSONObject history = get("traffic/history");
    require(history.isNull("samplingError") && history.getLong("uploadTotal") >= 49152
            && history.getInt("rowCount") > 0, "missing sampled traffic");
    log("traffic history rows=" + history.getLong("rowCount") + " upload=" + history.getLong("uploadTotal")
        + " download=" + history.getLong("downloadTotal"));
    post("disconnect", null);
    JSONObject status = get("status");
    require(!status.getBoolean("connected") && status.isNull("tunnelFd"), "fd retained");
    log("DISCONNECTED UI WINDOW");
    boolean finished = false;
    for (int i = 0; i < 240; i++) {
      HttpURLConnection sync =
          (HttpURLConnection) new URL(BASE + "/test/ui-finished").openConnection(Proxy.NO_PROXY);
      try {
        if (sync.getResponseCode() == 200) {
          finished = true;
          break;
        }
      } finally {
        sync.disconnect();
      }
      Thread.sleep(1000);
    }
    require(finished, "UI inspection timeout");
    JSONObject counts = get("status").getJSONObject("httpResponses");
    for (java.util.Iterator<String> it = counts.keys(); it.hasNext();) {
      require(Integer.parseInt(it.next()) < 400, "loopback HTTP failures " + counts);
    }
    log("Loopback HTTP counts " + counts + "; 4xx=0; 5xx=0");
    log("ACCEPTANCE DONE");
  }
  @Override
  public void onStart() {
    Bundle result = new Bundle();
    int code = 0;
    try {
      runChecks();
      code = -1;
    } catch (Exception e) {
      result.putString("stream", "FAIL " + step + " " + e.getClass().getSimpleName() + " " + e.getMessage());
    }
    finish(code, result);
  }
}
