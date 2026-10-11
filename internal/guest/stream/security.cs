namespace AMCStream {
using System;
using System.IO;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
internal sealed class SecurityPrivilege : IDisposable {
    [StructLayout(LayoutKind.Sequential)] struct Luid { public uint Low;public int High; }
    [StructLayout(LayoutKind.Sequential)] struct Privileges {public uint Count;public Luid ID;public uint Attributes;}
    [DllImport("kernel32.dll")] static extern IntPtr GetCurrentProcess();
    [DllImport("advapi32.dll",SetLastError=true)] static extern bool OpenProcessToken(IntPtr process,uint access,out SafeFileHandle token);
    [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool LookupPrivilegeValue(string system,string name,out Luid id);
    [DllImport("advapi32.dll",SetLastError=true)] static extern bool AdjustTokenPrivileges(SafeFileHandle token,bool disable,ref Privileges state,uint length,out Privileges previous,out uint needed);
    SafeFileHandle token;Privileges previous;
    internal SecurityPrivilege() {
        if(!OpenProcessToken(GetCurrentProcess(),0x28,out token))throw new IOException("security_privilege_unavailable");
        try {
            Luid id;if(!LookupPrivilegeValue(null,"SeSecurityPrivilege",out id))throw new IOException("security_privilege_unavailable");
            var state=new Privileges{Count=1,ID=id,Attributes=2};uint needed;
            if(!AdjustTokenPrivileges(token,false,ref state,(uint)Marshal.SizeOf(typeof(Privileges)),out previous,out needed)||Marshal.GetLastWin32Error()!=0)throw new IOException("security_privilege_unavailable");
        } catch{token.Dispose();throw;}
    }
    public void Dispose(){
        try {Privileges ignored;uint needed;
            if(!AdjustTokenPrivileges(token,false,ref previous,(uint)Marshal.SizeOf(typeof(Privileges)),out ignored,out needed)||Marshal.GetLastWin32Error()!=0)throw new IOException("security_privilege_restore_failed");
        } finally {token.Dispose();}
    }
}
}
