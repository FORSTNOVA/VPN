package com.smartvpn.acceptance;
import java.net.*;
import java.nio.charset.StandardCharsets;
public class TunnelProbe {
  public static void main(String[] args) throws Exception {
    if (args.length == 1 && (args[0].equals("udp-baseline") || args[0].equals("udp-protected"))) {
      boolean protectedPath=args[0].equals("udp-protected");
      InetAddress host=InetAddress.getByName("10.0.2.2");
      for(int port:new int[]{38487,443,853,3478,5349,19302,19309}) {
        byte[] request=("smartvpn-"+port).getBytes(StandardCharsets.US_ASCII);
        boolean expectReply=!protectedPath || port==38487;
        try(DatagramSocket socket=new DatagramSocket()){
          socket.connect(host,port);
          socket.setSoTimeout(1200);
          socket.send(new DatagramPacket(request,request.length));
          DatagramPacket reply=new DatagramPacket(new byte[32],32);
          try {
            socket.receive(reply);
            if (!expectReply || !java.util.Arrays.equals(request,java.util.Arrays.copyOf(reply.getData(),reply.getLength())))
              throw new AssertionError("unexpected UDP reply on "+port);
          } catch(SocketTimeoutException | PortUnreachableException error) {
            if(expectReply)throw error;
          }
        }
        System.out.println("UDP "+port+(expectReply?" exact reply":" blocked"));
      }
      return;
    }
    if (args.length == 1 && (args[0].equals("recovery") || args[0].equals("dns") || args[0].equals("rules"))) {
      byte[] q = new byte[] {18,52,1,0,0,1,0,0,0,0,0,0,7,'b','r','o','w','s','e','r',
          7,'e','x','a','m','p','l','e',0,0,1,0,1};
      try (DatagramSocket sock=new DatagramSocket()) {
        sock.setSoTimeout(3000);
        sock.connect(InetAddress.getByName("198.18.0.2"),53);
        long start=System.nanoTime();
        sock.send(new DatagramPacket(q,q.length));
        byte[] buf=new byte[512];
        DatagramPacket reply=new DatagramPacket(buf,buf.length);
        sock.receive(reply);
        int n=reply.getLength();
        if (buf[0]!=18 || buf[1]!=52 || buf[7]!=1 || buf[n-4]!=(byte)198 || buf[n-3]!=18)
          throw new AssertionError("unexpected fake DNS response");
        System.out.println("TUN DNS fake-ip="+InetAddress.getByAddress(java.util.Arrays.copyOfRange(buf,n-4,n)).getHostAddress()+" ms="+((System.nanoTime()-start)/1e6));
      }
      if (args[0].equals("recovery")) {
        java.security.KeyStore trust=java.security.KeyStore.getInstance(java.security.KeyStore.getDefaultType());
        trust.load(null);
        try (var ca=new java.io.FileInputStream("/data/local/tmp/recovery.crt")) {
          trust.setCertificateEntry("fixture",java.security.cert.CertificateFactory.getInstance("X.509").generateCertificate(ca));
        }
        var tm=javax.net.ssl.TrustManagerFactory.getInstance(javax.net.ssl.TrustManagerFactory.getDefaultAlgorithm());
        tm.init(trust);
        var context=javax.net.ssl.SSLContext.getInstance("TLS");
        context.init(null,tm.getTrustManagers(),null);
        var h=(javax.net.ssl.HttpsURLConnection)new URL("https://recovery.example/tunnel-proof").openConnection(Proxy.NO_PROXY);
        h.setSSLSocketFactory(context.getSocketFactory());
        h.setConnectTimeout(3000);
        h.setReadTimeout(3000);
        try {
          if (h.getResponseCode()!=200 || !new String(h.getInputStream().readAllBytes(), StandardCharsets.UTF_8).equals("SmartVPN-16k-tunnel"))
            throw new AssertionError("missing recovered tunnel response");
          System.out.println("Post-switch TUN HTTPS 200");
        } finally { h.disconnect(); }
        return;
      }
      if (args[0].equals("rules")) {
        for (String[] item : new String[][]{{"http://198.51.100.7/tunnel-proof","SmartVPN-16k-tunnel"},
                {"http://10.0.2.2:38471/lan","LAN direct fixture"}}) {
          HttpURLConnection h=(HttpURLConnection)new URL(item[0]).openConnection(Proxy.NO_PROXY);
          h.setConnectTimeout(5000);
          h.setReadTimeout(5000);
          try {
            if (h.getResponseCode()!=200 || !new String(h.getInputStream().readAllBytes(),StandardCharsets.UTF_8).equals(item[1]))
              throw new AssertionError("wrong TUN response");
            System.out.println(item[1]+" HTTP 200");
          } finally { h.disconnect(); }
        }
        return;
      }
      // This suffix is filtered, so the answer has to come from a real upstream.
      byte[] filtered = new byte[] {18,53,1,0,0,1,0,0,0,0,0,0,3,'w','w','w',15,
          'm','s','f','t','c','o','n','n','e','c','t','t','e','s','t',3,'c','o','m',0,0,1,0,1};
      try (DatagramSocket sock=new DatagramSocket()) {
        sock.connect(InetAddress.getByName("198.18.0.2"),53);
        sock.setSoTimeout(8000);
        sock.send(new DatagramPacket(filtered,filtered.length));
        byte[] buf=new byte[1500];
        DatagramPacket reply=new DatagramPacket(buf,buf.length);
        sock.receive(reply);
        int n=reply.getLength();
        if (buf[0]!=18 || buf[1]!=53 || (buf[3]&15)!=0 || buf[7]==0 || (buf[n-4]==(byte)198 && buf[n-3]==18))
          throw new AssertionError("filtered query did not return a real address");
        System.out.println("filtered DNS via upstream passed");
      }
      for (String url : new String[]{"http://browser.example/tunnel","https://www.gstatic.com/generate_204"}) {
        HttpURLConnection h=(HttpURLConnection)new URL(url).openConnection(Proxy.NO_PROXY);
        h.setConnectTimeout(5000);
        h.setReadTimeout(5000);
        try {
          int status=h.getResponseCode();
          if (url.startsWith("http:") ? status!=200 || !new String(h.getInputStream().readAllBytes(),StandardCharsets.UTF_8).equals("SmartVPN domain tunnel") : status!=204)
            throw new AssertionError("unexpected domain response");
          System.out.println("TUN "+url+" HTTP "+status);
        } finally { h.disconnect(); }
      }
      // The fixture SOCKS server rejects this hostname. Successful TLS here
      // exercises the domestic suffix rule and its direct nameserver policy.
      HttpURLConnection direct=(HttpURLConnection)new URL("https://www.baidu.com/").openConnection(Proxy.NO_PROXY);
      direct.setConnectTimeout(6000);
      direct.setReadTimeout(6000);
      try {
        int code=direct.getResponseCode();
        if (code!=200) throw new AssertionError("domestic HTTPS status "+code);
        System.out.println("TUN domestic HTTPS DIRECT HTTP "+code);
      } finally { direct.disconnect(); }
      return;
    }
    if (args.length == 1 && args[0].equals("chain")) {
      HttpURLConnection h = (HttpURLConnection)new URL("http://198.51.100.7:38476/chain-target").openConnection(Proxy.NO_PROXY);
      h.setConnectTimeout(5000);
      h.setReadTimeout(7000);
      try {
        byte[] actual = h.getInputStream().readAllBytes();
        if (h.getResponseCode()!=200 || !java.util.Arrays.equals(actual, "SmartVPN multihop exact body".getBytes(StandardCharsets.UTF_8)))
          throw new AssertionError("chain response mismatch");
        System.out.println("TUN chain HTTP 200 exact bytes=" + actual.length);
      } finally {
        h.disconnect();
      }
      return;
    }
    for (int i = 0; i < 5; i++) {
      var h = (HttpURLConnection) new URL("http://198.51.100.7/tunnel-proof")
                  .openConnection(Proxy.NO_PROXY);
      h.setRequestProperty("Connection", "close");
      h.setConnectTimeout(3000);
      h.setReadTimeout(3000);
      long start = System.nanoTime();
      try {
        int status = h.getResponseCode();
        String answer = new String(h.getInputStream().readAllBytes(), StandardCharsets.UTF_8);
        if (status != 200 || !answer.equals("SmartVPN-16k-tunnel"))
          throw new AssertionError("wrong fixture response");
        System.out.println(
            "TUN HTTP " + status + " RTT " + ((System.nanoTime() - start) / 1_000_000) + " ms");
      } finally {
        h.disconnect();
      }
    }
    // Hold a measured flow open across multiple one-second history samples.
    try (Socket socket = new Socket("198.51.100.7", 80)) {
      socket.setSoTimeout(4000);
      var out = socket.getOutputStream();
      out.write(("POST /history-proof HTTP/1.1\r\nHost: fixture\r\n"
                    + "Content-Length: 65536\r\nConnection: close\r\n\r\n")
                    .getBytes(StandardCharsets.US_ASCII));
      for (int i = 0; i < 4; i++) {
        out.write(new byte[16384]);
        out.flush();
        Thread.sleep(1100);
      }
      String response = new String(socket.getInputStream().readAllBytes(), StandardCharsets.US_ASCII);
      if (!response.startsWith("HTTP/1.1 200") || !response.endsWith("OK"))
        throw new AssertionError("history transfer failed");
      System.out.println("Sustained tunnel transfer 65536 bytes HTTP 200");
    }
  }
}
