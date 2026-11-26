use anyhow::{Context, Result};
use std::{io, ptr};
use windows_sys::Win32::{
    Foundation::HANDLE, NetworkManagement::WindowsFilteringPlatform::*,
    System::Rpc::RPC_C_AUTHN_WINNT,
};

// A dynamic WFP session owns the filters, including on abnormal process exit.
pub struct DnsGuard(HANDLE);
// WFP engine handles are process-wide. The owning Kernel serializes access.
unsafe impl Send for DnsGuard {}

fn status(code: u32) -> io::Result<()> {
    if code == 0 {
        Ok(())
    } else {
        Err(io::Error::from_raw_os_error(code as i32))
    }
}

impl DnsGuard {
    pub fn install(tun_index: u32) -> Result<Self> {
        use windows_sys::Win32::NetworkManagement::{
            IpHelper::ConvertInterfaceIndexToLuid, Ndis::NET_LUID_LH,
        };
        let mut luid = NET_LUID_LH::default();
        unsafe { status(ConvertInterfaceIndexToLuid(tun_index, &mut luid)) }
            .context("读取 SmartVPN 网卡标识")?;
        let session = FWPM_SESSION0 {
            flags: FWPM_SESSION_FLAG_DYNAMIC,
            ..Default::default()
        };
        let mut handle = ptr::null_mut();
        unsafe {
            status(FwpmEngineOpen0(
                ptr::null(),
                RPC_C_AUTHN_WINNT,
                ptr::null(),
                &session,
                &mut handle,
            ))
        }
        .context("打开 Windows DNS 防泄漏过滤会话")?;
        let guard = Self(handle);
        unsafe { status(FwpmTransactionBegin0(handle, 0)) }?;
        let mut label: Vec<u16> = "SmartVPN DNS protection\0".encode_utf16().collect();
        for layer in [
            FWPM_LAYER_ALE_AUTH_CONNECT_V4,
            FWPM_LAYER_ALE_AUTH_CONNECT_V6,
        ] {
            for port in [53u16, 853] {
                let mut conditions = [
                    FWPM_FILTER_CONDITION0 {
                        fieldKey: FWPM_CONDITION_IP_REMOTE_PORT,
                        matchType: FWP_MATCH_EQUAL,
                        conditionValue: FWP_CONDITION_VALUE0 {
                            r#type: FWP_UINT16,
                            Anonymous: FWP_CONDITION_VALUE0_0 { uint16: port },
                        },
                    },
                    FWPM_FILTER_CONDITION0 {
                        fieldKey: FWPM_CONDITION_IP_LOCAL_INTERFACE,
                        matchType: FWP_MATCH_NOT_EQUAL,
                        conditionValue: FWP_CONDITION_VALUE0 {
                            r#type: FWP_UINT64,
                            Anonymous: FWP_CONDITION_VALUE0_0 {
                                uint64: unsafe { &mut luid.Value },
                            },
                        },
                    },
                ];
                let filter = FWPM_FILTER0 {
                    displayData: FWPM_DISPLAY_DATA0 {
                        name: label.as_mut_ptr(),
                        description: ptr::null_mut(),
                    },
                    layerKey: layer,
                    subLayerKey: FWPM_SUBLAYER_UNIVERSAL,
                    action: FWPM_ACTION0 {
                        r#type: FWP_ACTION_BLOCK,
                        ..Default::default()
                    },
                    numFilterConditions: conditions.len() as u32,
                    filterCondition: conditions.as_mut_ptr(),
                    ..Default::default()
                };
                unsafe {
                    status(FwpmFilterAdd0(
                        handle,
                        &filter,
                        ptr::null_mut(),
                        ptr::null_mut(),
                    ))
                }
                .with_context(|| format!("添加非 TUN 网卡的 DNS 端口 {port} 过滤器"))?;
            }
        }
        unsafe { status(FwpmTransactionCommit0(handle)) }
            .context("启用 Windows DNS 防泄漏过滤器")?;
        Ok(guard)
    }
}
impl Drop for DnsGuard {
    fn drop(&mut self) {
        // Closing a session aborts any open transaction and deletes dynamic filters.
        unsafe {
            FwpmEngineClose0(self.0);
        }
    }
}
