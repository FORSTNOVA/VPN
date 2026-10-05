package com.smartvpn.client;

import android.security.keystore.KeyGenParameterSpec;
import android.security.keystore.KeyProperties;
import android.util.Base64;

import java.security.KeyStore;
import java.util.Arrays;

import javax.crypto.Cipher;
import javax.crypto.KeyGenerator;
import javax.crypto.SecretKey;
import javax.crypto.spec.GCMParameterSpec;

/**
 * The Java side of the native boundary, and the three things only Java can do.
 *
 * <p>The kernel, the scheduling, the health state machine and the local API are
 * one Go library, loaded here. Three of its needs cannot be met from Go on this
 * platform:
 *
 * <ul>
 *   <li>protecting a socket, so the kernel's own connections to a proxy server
 *       do not re-enter the tunnel those connections carry;
 *   <li>sealing a subscription URL with a key the app can use but not read;
 *   <li>knowing what the VpnService established — the interface, the routes and
 *       the resolver — which is the system's answer, not the app's.
 * </ul>
 *
 * <p>This class is written in Java rather than Kotlin because it is the one
 * place where a method's exact JVM shape decides whether the native library can
 * find it at all: JNI resolves a method by its name and signature, and plain
 * {@code static} methods leave nothing to interpretation.
 */
public final class CoreBridge {

    /** The class name the native side is told, so it can find the callbacks below. */
    public static final String CLASS_NAME = "com/smartvpn/client/CoreBridge";

    private static final String KEY_ALIAS = "smartvpn-subscription";
    private static final String KEYSTORE = "AndroidKeyStore";
    private static final String TRANSFORMATION = "AES/GCM/NoPadding";
    private static final int GCM_TAG_BITS = 128;
    private static final int GCM_IV_BYTES = 12;

    /** The service whose tunnel is up, and which can protect a socket for it. */
    private static SmartVpnService service;

    static {
        System.loadLibrary("smartvpn");
    }

    private CoreBridge() {}

    // ---------------------------------------------------------------- native

    /**
     * Starts the service and answers with a JSON bootstrap record: the port and
     * token the window talks to, or a reason it refused. Calling it twice is
     * harmless and answers with the same address.
     */
    public static native String start(String dataDir, String bridgeClass);

    /** Stops the service and ends any connection with it. */
    public static native void stop();

    /**
     * Records the tunnel the VpnService established, or reports that it is
     * gone. The descriptor is the tunnel's, and it is only meaningful in this
     * process, which is why the tunnel and the kernel live in the same one.
     */
    public static native String setTunnel(boolean authorized, int fd, String info);

    // ------------------------------------------------- Java for the native side

    /**
     * Called by the kernel's own dialer before it connects anywhere, once per
     * socket. Returning false makes that connection fail rather than be routed
     * into its own tunnel, which is the failure that is safe to have: a loop
     * would carry nothing at all.
     */
    public static boolean protect(int fd) {
        SmartVpnService current = service;
        return current != null && current.protectSocket(fd);
    }

    /**
     * Called by the Go side when it needs the tunnel's description now, rather
     * than the one it was given when the tunnel came up.
     */
    public static String describeTunnelNow() {
        SmartVpnService current = service;
        return current == null ? "{}" : current.describeTunnel();
    }

    /**
     * Called by the Go side when the kernel has gone: the tunnel has to follow
     * it, because an interface with nobody reading it is what a device with no
     * network looks like. Stopping the service is what removes a tunnel.
     */
    public static void dropTunnel() {
        SmartVpnService current = service;
        if (current != null) {
            current.stopSelf();
        }
    }

    /**
     * Seals a secret with a key held by the Android keystore.
     *
     * <p>The key is created on first use, is stored by the system rather than by
     * this app, and cannot be read back — only used. What comes out is the IV
     * followed by the ciphertext, base64-encoded; the prefix that marks it as
     * sealed is written by the Go side, which owns the stored form.
     */
    public static String seal(String plain) {
        try {
            Cipher cipher = Cipher.getInstance(TRANSFORMATION);
            cipher.init(Cipher.ENCRYPT_MODE, key());
            byte[] iv = cipher.getIV();
            byte[] sealed = cipher.doFinal(plain.getBytes("UTF-8"));
            byte[] joined = new byte[iv.length + sealed.length];
            System.arraycopy(iv, 0, joined, 0, iv.length);
            System.arraycopy(sealed, 0, joined, iv.length, sealed.length);
            return Base64.encodeToString(joined, Base64.NO_WRAP);
        } catch (Exception error) {
            // Nothing is stored rather than something stored in the clear: the
            // caller reports the failure to the user instead.
            return null;
        }
    }

    /** Opens what {@link #seal} produced. Answers null if it cannot be opened. */
    public static String open(String sealed) {
        try {
            byte[] joined = Base64.decode(sealed, Base64.NO_WRAP);
            if (joined.length <= GCM_IV_BYTES) {
                return null;
            }
            byte[] iv = Arrays.copyOfRange(joined, 0, GCM_IV_BYTES);
            byte[] body = Arrays.copyOfRange(joined, GCM_IV_BYTES, joined.length);
            Cipher cipher = Cipher.getInstance(TRANSFORMATION);
            cipher.init(Cipher.DECRYPT_MODE, key(), new GCMParameterSpec(GCM_TAG_BITS, iv));
            return new String(cipher.doFinal(body), "UTF-8");
        } catch (Exception error) {
            return null;
        }
    }

    private static SecretKey key() throws Exception {
        KeyStore store = KeyStore.getInstance(KEYSTORE);
        store.load(null);
        if (store.containsAlias(KEY_ALIAS)) {
            return (SecretKey) store.getKey(KEY_ALIAS, null);
        }
        KeyGenerator generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, KEYSTORE);
        generator.init(new KeyGenParameterSpec.Builder(KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT | KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build());
        return generator.generateKey();
    }

    // ------------------------------------------------------- service plumbing

    static void attachService(SmartVpnService running) {
        service = running;
    }

    static void detachService(SmartVpnService finished) {
        if (service == finished) {
            service = null;
        }
    }
}
