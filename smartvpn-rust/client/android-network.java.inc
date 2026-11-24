    private ConnectivityManager.NetworkCallback uplinkCallback;
    private android.os.HandlerThread uplinkThread;

    private void watchUplink(ConnectivityManager manager) {
        uplinkThread = new android.os.HandlerThread("SmartVPN-uplink");
        uplinkThread.start();
        android.os.Handler handler = new android.os.Handler(uplinkThread.getLooper());
        uplinkCallback = new ConnectivityManager.NetworkCallback() {
            private Network previous;
            private Network candidate;
            private final Runnable changed = () -> {
                if (candidate != null && !candidate.equals(previous)) {
                    try {
                        CoreBridge.notifyNetworkChanged();
                        previous = candidate;
                        android.util.Log.i("SmartVPN", "Uplink recovery completed");
                    } catch (IllegalStateException error) {
                        android.util.Log.e("SmartVPN", "Uplink recovery failed", error);
                    }
                }
            };

            @Override public void onCapabilitiesChanged(Network network, NetworkCapabilities caps) {
                if (!caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) return;
                if (previous == null) previous = network;
                if (network.equals(candidate)) return;
                candidate = network;
                handler.removeCallbacks(changed);
                if (!network.equals(previous)) {
                    android.util.Log.i("SmartVPN", "New physical uplink available");
                    handler.postDelayed(changed, 250);
                }
            }

            @Override public void onLost(Network network) {
                if (network.equals(candidate)) {
                    candidate = null;
                    handler.removeCallbacks(changed);
                }
            }
        };
        if (android.os.Build.VERSION.SDK_INT >= 31) {
            NetworkRequest physical = new NetworkRequest.Builder()
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
                    .build();
            manager.registerBestMatchingNetworkCallback(physical, uplinkCallback, handler);
        } else {
            manager.registerDefaultNetworkCallback(uplinkCallback, handler);
        }
    }

    private void unwatchUplink(ConnectivityManager manager) {
        if (uplinkCallback != null) {
            manager.unregisterNetworkCallback(uplinkCallback);
            uplinkThread.quit();
            uplinkCallback = null;
        }
    }

