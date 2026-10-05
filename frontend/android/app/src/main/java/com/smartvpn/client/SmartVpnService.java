package com.smartvpn.client;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.net.ConnectivityManager;
import android.net.LinkProperties;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.net.RouteInfo;
import android.net.VpnService;
import android.os.Build;
import android.os.ParcelFileDescriptor;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.List;

/**
 * The tunnel, and the foreground service that keeps it.
 *
 * <p>This is the half of the Android design the platform insists on: only a
 * VpnService can create the interface, and only the app's own process can hold
 * its file descriptor. The kernel is linked into that same process and is given
 * the descriptor, which is why the service and the kernel are one process and
 * why the tunnel is reported to the Go side the moment it exists rather than
 * being discovered.
 *
 * <p>The routes, the resolver and the interface name are the system's: this
 * class asks for them and then reads back what it was given, because that is
 * what the checks page has to report.
 */
public class SmartVpnService extends VpnService {

    public static final String ACTION_START = "com.smartvpn.client.START";
    public static final String ACTION_STOP = "com.smartvpn.client.STOP";

    private static final String CHANNEL_ID = "smartvpn";
    private static final int NOTIFICATION_ID = 1;

    /** The address of this side of the tunnel. It is carried by nothing else. */
    private static final String TUN_ADDRESS = "172.16.0.1";
    private static final int TUN_PREFIX = 30;

    /**
     * The resolver applications are given. It is inside the fake-ip range the
     * kernel hands out (198.18.0.0/16 in the generated configuration), so a
     * lookup arrives at the tunnel, is hijacked there and answered by the
     * kernel's own resolver — which is what makes the answers impossible to
     * obtain behind the tunnel's back.
     */
    private static final String TUN_DNS = "198.18.0.2";

    /**
     * Where one establishment's answer goes. The window asks for the tunnel
     * over the method channel and has to be answered when the system has
     * finished granting it, which is one callback later.
     */
    public interface Listener {
        void onTunnel(boolean established, String message);
    }

    private static Listener pending;

    static void setListener(Listener listener) {
        pending = listener;
    }

    private ParcelFileDescriptor tunnel;
    /** The tunnel's descriptor, which the kernel owns from the moment it exists. */
    private int tunnelFd;
    private ConnectivityManager.NetworkCallback vpnCallback;

    @Override
    public void onCreate() {
        super.onCreate();
        CoreBridge.attachService(this);
        createChannel();
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String action = intent == null ? ACTION_START : intent.getAction();
        if (ACTION_STOP.equals(action)) {
            answer(false, "已断开");
            stopSelf();
            return START_NOT_STICKY;
        }
        startForeground(NOTIFICATION_ID, notification("正在建立隧道"));
        if (tunnel != null) {
            answer(true, "");
            return START_NOT_STICKY;
        }
        String failure = establish();
        answer(failure == null, failure == null ? "" : failure);
        if (failure != null) {
            stopSelf();
        }
        return START_NOT_STICKY;
    }

    /**
     * Asks the system for the tunnel and hands the descriptor to the kernel.
     *
     * <p>Returns null on success, or a reason in the user's language. Failure is
     * answered rather than thrown: the window is waiting, and a dialog that
     * never gets an answer is worse than one that says what went wrong.
     */
    private String establish() {
        Builder builder = new Builder();
        builder.setSession("SmartVPN");
        builder.setMtu(1500);
        try {
            builder.addAddress(TUN_ADDRESS, TUN_PREFIX);
            builder.addDnsServer(TUN_DNS);
            builder.addRoute("0.0.0.0", 0);
            // IPv6 is routed into the tunnel as well, without an address of its
            // own: the resolver hands out no AAAA records on purpose, so nothing
            // resolves to an address the tunnel cannot carry, and leaving the
            // family unrouted would let an address that is already known leave
            // the device unproxied.
            builder.addRoute("::", 0);
        } catch (Exception error) {
            return "无法建立隧道：" + error.getMessage();
        }
        try {
            // This app's own traffic stays outside the tunnel, and that is
            // deliberate rather than a convenience: the subscription has to be
            // fetched before there is any node to fetch it through, and the
            // kernel's connections to the proxy servers are the tunnel's own
            // upstream — they cannot be carried by what they carry. Sockets are
            // protected as well, so the guarantee does not rest on the exclusion
            // being honoured.
            builder.addDisallowedApplication(getPackageName());
        } catch (Exception error) {
            // Recorded rather than fatal: without the exclusion the protected
            // sockets still work, and what would stop working is fetching a
            // subscription while connected.
            android.util.Log.w("SmartVPN", "could not exclude this app from its own tunnel: " + error);
        }
        ParcelFileDescriptor established = builder.establish();
        if (established == null) {
            return "系统拒绝建立隧道：可能是另一个 VPN 正在运行，或授权已被撤销";
        }
        // The descriptor is handed over rather than shared. From here the kernel
        // owns it and closes it when it stops, and only a detached descriptor
        // may be closed that way: Android's file-descriptor checker aborts the
        // whole process when native code closes one that a ParcelFileDescriptor
        // still claims, which is precisely what the kernel does.
        tunnelFd = established.detachFd();
        tunnel = established;
        android.util.Log.i("SmartVPN", "tunnel established, descriptor " + tunnelFd);
        watchNetworks();
        String report = describeTunnel();
        String refusal = CoreBridge.setTunnel(true, tunnelFd, report);
        if (refusal == null || refusal.isEmpty()) {
            updateNotification("已连接");
            return null;
        }
        String reason = readError(refusal);
        closeTunnel();
        return "隧道已建立，但本地服务无法使用它：" + reason;
    }

    /**
     * describeTunnel reads back what the system made: the interface, the routes
     * on it and the resolver it points at. The checks page reports these as the
     * system's answers, which is what they are.
     *
     * <p>It is read on demand rather than remembered from a callback. The system
     * describes a tunnel in more than one step — the interface appears before
     * the routes are on it — so a description taken once, when it is
     * established, can say there are no routes while the tunnel is carrying
     * everything, which is the wrong thing for a checks page to believe.
     */
    String describeTunnel() {
        JSONObject report = new JSONObject();
        try {
            LinkProperties properties = activeProperties();
            report.put("interface", properties == null || properties.getInterfaceName() == null
                    ? "" : properties.getInterfaceName());
            JSONArray routes = new JSONArray();
            JSONArray dns = new JSONArray();
            if (properties != null) {
                for (RouteInfo route : properties.getRoutes()) {
                    routes.put(route.getDestination().toString());
                }
                for (java.net.InetAddress server : properties.getDnsServers()) {
                    dns.put(server.getHostAddress());
                }
            }
            report.put("routes", routes);
            report.put("dns", dns);
            android.util.Log.w("SmartVPN", "tunnel network: interface " + report.optString("interface")
                    + ", routes " + routes + ", dns " + dns);
        } catch (Exception error) {
            android.util.Log.w("SmartVPN", "could not describe the tunnel: " + error);
        }
        return report.toString();
    }

    private LinkProperties activeProperties() {
        ConnectivityManager manager = getSystemService(ConnectivityManager.class);
        if (manager == null) {
            return null;
        }
        for (Network network : manager.getAllNetworks()) {
            NetworkCapabilities capabilities = manager.getNetworkCapabilities(network);
            if (capabilities == null
                    || !capabilities.hasTransport(NetworkCapabilities.TRANSPORT_VPN)) {
                continue;
            }
            LinkProperties properties = manager.getLinkProperties(network);
            if (properties != null && properties.getInterfaceName() != null
                    && properties.getInterfaceName().startsWith("tun")) {
                return properties;
            }
        }
        return null;
    }

    /**
     * reportTunnel hands the Go side what the system made of this tunnel: the
     * interface, the routes on it and the resolver it points at.
     *
     * <p>It is called again whenever the network changes, rather than once when
     * the tunnel is established, because the tunnel is not fully described the
     * moment it exists: the system registers the VPN network a moment later, and
     * a report taken too early says there are no routes — which is exactly the
     * wrong thing for a checks page to believe.
     */
    private void reportTunnel() {
        if (tunnel == null) {
            return;
        }
        android.util.Log.i("SmartVPN", "reporting tunnel, descriptor " + tunnelFd);
        CoreBridge.setTunnel(true, tunnelFd, describeTunnel());
    }

    /**
     * watchNetworks tells the system which physical network the tunnel rides on,
     * and keeps telling it as that changes.
     *
     * <p>Switching between Wi-Fi and mobile data is the case this exists for: a
     * tunnel that does not say what it is riding on is torn down or left without
     * an upstream at the moment of the switch.
     */
    /**
     * watchNetworks watches the tunnel's own network, so that what the system
     * made of it can be reported as it becomes known.
     *
     * <p>Nothing here sets the tunnel's underlying network. Android picks that
     * itself when it is left alone, and picks it better: the app's own view of
     * the default network is not the tunnel's view of the network it rides on,
     * and naming the wrong one binds protected sockets to a network that cannot
     * carry them — every connection to a proxy server then waits for a timeout.
     */
    private void watchNetworks() {
        ConnectivityManager manager = getSystemService(ConnectivityManager.class);
        if (manager == null) {
            return;
        }
        NetworkRequest tunnelRequest = new NetworkRequest.Builder()
                .addTransportType(NetworkCapabilities.TRANSPORT_VPN)
                .build();
        vpnCallback = new ConnectivityManager.NetworkCallback() {
            @Override
            public void onAvailable(Network network) {
                reportTunnel();
            }

            @Override
            public void onLinkPropertiesChanged(Network network, LinkProperties properties) {
                reportTunnel();
            }
        };
        try {
            manager.registerNetworkCallback(tunnelRequest, vpnCallback);
        } catch (Exception error) {
            android.util.Log.w("SmartVPN", "could not watch the tunnel's network: " + error);
        }
    }

    private void unwatchNetworks() {
        ConnectivityManager manager = getSystemService(ConnectivityManager.class);
        if (manager == null || vpnCallback == null) {
            return;
        }
        try {
            manager.unregisterNetworkCallback(vpnCallback);
        } catch (Exception error) {
            android.util.Log.w("SmartVPN", "could not stop watching the tunnel: " + error);
        }
        vpnCallback = null;
    }

    /** protectSocket is called by the kernel for every socket it dials. */
    boolean protectSocket(int fd) {
        return protect(fd);
    }

    @Override
    public void onRevoke() {
        // The system takes the tunnel back when another VPN is started or the
        // user withdraws the authorisation. The Go side is told before the
        // descriptor goes: a kernel left running against a closed tunnel would
        // report a connection that carries nothing.
        super.onRevoke();
        android.util.Log.i("SmartVPN", "the system revoked the tunnel");
        closeTunnel();
        stopSelf();
    }

    @Override
    public void onDestroy() {
        closeTunnel();
        CoreBridge.detachService(this);
        answer(false, "已断开");
        super.onDestroy();
    }

    private void closeTunnel() {
        unwatchNetworks();
        if (tunnel != null) {
            // The Go side is told first: it decides whether the descriptor still
            // needs closing, which is the case when no kernel ever took it.
            android.util.Log.i("SmartVPN", "closing tunnel, descriptor " + tunnelFd);
            CoreBridge.setTunnel(false, 0, "{}");
            try {
                // The descriptor was detached, so this closes the object and not
                // the tunnel; the interface itself goes when the service does.
                tunnel.close();
            } catch (Exception error) {
                android.util.Log.w("SmartVPN", "could not close the tunnel: " + error);
            }
            tunnel = null;
            tunnelFd = 0;
        }
    }

    private void answer(boolean established, String message) {
        Listener listener = pending;
        pending = null;
        if (listener != null) {
            listener.onTunnel(established, message);
        }
    }

    // ------------------------------------------------------------ notification

    private void createChannel() {
        NotificationManager manager = getSystemService(NotificationManager.class);
        if (manager == null) {
            return;
        }
        NotificationChannel channel = new NotificationChannel(CHANNEL_ID, "连接状态",
                NotificationManager.IMPORTANCE_LOW);
        channel.setDescription("SmartVPN 的连接状态");
        manager.createNotificationChannel(channel);
    }

    private Notification notification(String text) {
        Intent open = new Intent(this, MainActivity.class);
        open.setFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP);
        PendingIntent content = PendingIntent.getActivity(this, 0, open,
                PendingIntent.FLAG_IMMUTABLE);

        Intent stop = new Intent(this, SmartVpnService.class).setAction(ACTION_STOP);
        PendingIntent disconnect = PendingIntent.getService(this, 1, stop,
                PendingIntent.FLAG_IMMUTABLE);

        return new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("SmartVPN")
                .setContentText(text)
                .setSmallIcon(R.mipmap.ic_launcher)
                .setContentIntent(content)
                .addAction(new Notification.Action.Builder(null, "断开", disconnect).build())
                .setOngoing(true)
                .build();
    }

    private void updateNotification(String text) {
        NotificationManager manager = getSystemService(NotificationManager.class);
        if (manager != null) {
            manager.notify(NOTIFICATION_ID, notification(text));
        }
    }

    /** readError pulls the reason out of the bootstrap record the core answers with. */
    private static String readError(String json) {
        try {
            String error = new JSONObject(json).optString("error", "");
            return error.isEmpty() ? json : error;
        } catch (Exception ignored) {
            return json;
        }
    }

    /** startService is what the window calls to bring the tunnel up. */
    static void start(Context context, Listener listener) {
        setListener(listener);
        Intent intent = new Intent(context, SmartVpnService.class).setAction(ACTION_START);
        // A foreground service has to be started as one from Android 8 on, and
        // from Android 12 starting one from the background is refused; both are
        // handled by asking for it the way the platform wants.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            context.startForegroundService(intent);
        } else {
            context.startService(intent);
        }
    }

    static void stop(Context context) {
        context.stopService(new Intent(context, SmartVpnService.class));
    }
}
