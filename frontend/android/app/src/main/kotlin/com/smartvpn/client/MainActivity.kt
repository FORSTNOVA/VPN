package com.smartvpn.client

import android.Manifest
import android.app.Activity
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import org.json.JSONObject

/**
 * The window's side of the host.
 *
 * On Windows the window starts a service process and reads its address from the
 * service's own output. Here there is no second process to start: this starts
 * the one Go library, asks the system for the tunnel when the user connects,
 * and answers the window either way. The two answers the window gets — the
 * service's address and whether the tunnel came up — are the only things it
 * cannot find out for itself.
 */
class MainActivity : FlutterActivity() {

    /** The window's request that is waiting for the system's consent. */
    private var pendingConnect: MethodChannel.Result? = null

    override fun configureFlutterEngine(engine: FlutterEngine) {
        super.configureFlutterEngine(engine)
        MethodChannel(engine.dartExecutor.binaryMessenger, CHANNEL)
            .setMethodCallHandler { call, result ->
                when (call.method) {
                    "service" -> result.success(startService())
                    "connect" -> connect(result)
                    "disconnect" -> {
                        SmartVpnService.stop(this)
                        result.success(null)
                    }
                    else -> result.notImplemented()
                }
            }
    }

    /**
     * startService brings the local service up and answers with its address as
     * a JSON bootstrap record — the same record the Windows service prints, so
     * the window has one shape to read. Calling it again answers with the same
     * address rather than starting a second service.
     */
    private fun startService(): String =
        CoreBridge.start(filesDir.absolutePath, CoreBridge.CLASS_NAME)
            ?: JSONObject().put("error", "本地服务没有启动").toString()

    /**
     * connect asks for what this platform requires before a tunnel can exist:
     * the notification permission, so the connection is visible while it runs,
     * and then the user's authorisation of the VPN.
     */
    private fun connect(result: MethodChannel.Result) {
        if (pendingConnect != null) {
            result.error("busy", "上一次连接请求还没有结束", null)
            return
        }
        pendingConnect = result
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQUEST_NOTIFICATIONS)
            return
        }
        requestTunnel()
    }

    private fun requestTunnel() {
        // A non-null answer means the system wants the user's consent first, and
        // this activity is the only thing that can ask for it.
        val consent = VpnService.prepare(this)
        if (consent != null) {
            startActivityForResult(consent, REQUEST_VPN)
            return
        }
        openTunnel()
    }

    private fun openTunnel() {
        SmartVpnService.start(this) { established, message ->
            val answer = JSONObject().put("ok", established).put("message", message).toString()
            pendingConnect?.success(answer)
            pendingConnect = null
        }
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (requestCode == REQUEST_VPN) {
            if (resultCode == Activity.RESULT_OK) {
                openTunnel()
            } else {
                failConnect("用户没有授权 VPN，连接已取消")
            }
            return
        }
        super.onActivityResult(requestCode, resultCode, data)
    }

    override fun onRequestPermissionsResult(
        requestCode: Int,
        permissions: Array<out String>,
        grantResults: IntArray,
    ) {
        if (requestCode == REQUEST_NOTIFICATIONS) {
            // The answer does not decide whether the tunnel can be built: a
            // notification that is not shown is a smaller problem than a
            // connection that is not made.
            requestTunnel()
            return
        }
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
    }

    private fun failConnect(message: String) {
        pendingConnect?.success(JSONObject().put("ok", false).put("message", message).toString())
        pendingConnect = null
    }

    companion object {
        private const val CHANNEL = "smartvpn/host"
        private const val REQUEST_VPN = 1001
        private const val REQUEST_NOTIFICATIONS = 1002
    }
}
