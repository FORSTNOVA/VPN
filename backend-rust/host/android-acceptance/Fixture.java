package com.smartvpn.acceptance;

import java.io.*;
import java.net.*;
import java.nio.charset.StandardCharsets;
import java.util.UUID;

// Local VLESS test node. Only the single test destination receives a reply.
// All other destinations are closed locally.
final class Fixture implements AutoCloseable {
    final UUID id = UUID.randomUUID();
    final ServerSocket server = new ServerSocket(0, 16, InetAddress.getByName("127.0.0.1"));
    volatile String nonce;
    Fixture() throws IOException {
        new Thread(() -> {
            while (!server.isClosed()) {
                try {
                    Socket s = server.accept();
                    new Thread(() -> answer(s)).start();
                } catch (IOException e) {
                    break;
                }
            }
        }).start();
    }
    void answer(Socket socket) {
        try (Socket s = socket) {
            s.setSoTimeout(5000);
            DataInputStream in = new DataInputStream(s.getInputStream());
            if (in.readUnsignedByte() != 0 || in.readLong() != id.getMostSignificantBits()
                || in.readLong() != id.getLeastSignificantBits())
                return;
            in.readFully(new byte[in.readUnsignedByte()]);
            if (in.readUnsignedByte() != 1 || in.readUnsignedShort() != 18061
                || in.readUnsignedByte() != 1 || in.readInt() != 0xc633643d)
                return;
            String expected = nonce;
            if (expected == null)
                return;
            byte[] challenge = (expected + "\n").getBytes(StandardCharsets.US_ASCII);
            byte[] actual = new byte[challenge.length];
            in.readFully(actual);
            if (!java.util.Arrays.equals(actual, challenge))
                return;
            OutputStream out = s.getOutputStream();
            out.write(new byte[] {0, 0});
            out.write(("TUN " + expected + "\n").getBytes(StandardCharsets.US_ASCII));
        } catch (IOException ignored) {
        }
    }
    public void close() throws IOException {
        server.close();
    }
}
