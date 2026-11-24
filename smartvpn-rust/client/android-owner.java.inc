    static native void notifyNetworkChanged();
    static native String readGameBypass(String directory);

    public static String connectionOwner(String network, String src, int srcPort,
                                         String dst, int dstPort) throws java.net.UnknownHostException {
        if (android.os.Build.VERSION.SDK_INT < 29) return null;
        SmartVpnService running = service;
        android.net.ConnectivityManager manager = running.getSystemService(android.net.ConnectivityManager.class);
        int protocol = network.equals("tcp") ? android.system.OsConstants.IPPROTO_TCP : android.system.OsConstants.IPPROTO_UDP;
        int uid = manager.getConnectionOwnerUid(protocol,
                new java.net.InetSocketAddress(android.net.InetAddresses.parseNumericAddress(src), srcPort),
                new java.net.InetSocketAddress(android.net.InetAddresses.parseNumericAddress(dst), dstPort));
        if (uid < 0) return null;
        String[] packages = running.getPackageManager().getPackagesForUid(uid);
        return packages == null ? "uid:" + uid : String.join(", ", packages);
    }

