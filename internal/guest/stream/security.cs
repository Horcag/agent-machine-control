namespace AMCStream {
using System;
using System.IO;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using Microsoft.Win32.SafeHandles;
internal sealed class NativeFailure : IOException {
    internal readonly string Stage;internal readonly uint Code;
    internal NativeFailure(string stage,uint code){Stage=stage;Code=code;}
}
internal static class Failure {
    // Exit diagnostics are bounded metadata, never a successful protocol reply.
    internal static uint TimerExit(Exception error,string stage) {
        var native=error as NativeFailure;uint code=native==null?0u:native.Code;
        if(native!=null)stage=native.Stage;
        uint index;
        switch(stage) {
            case "runspace_open":index=1;break;case "request":index=2;break;
            case "observe":index=3;break;case "put":index=4;break;case "get":index=5;break;
            case "exec":index=6;break;case "remove_owned":index=7;break;
            case "security_token":index=8;break;case "security_lookup":index=9;break;
            case "security_enable":index=10;break;case "security_restore":index=11;break;
            case "ancestor_open":index=12;break;case "workspace_open":index=13;break;
            case "file_open":index=14;break;case "metadata_read":index=15;break;
            case "file_id_read":index=16;break;case "security_size":index=17;break;
            case "security_read":index=18;break;case "security_parse":index=19;break;
            case "file_commit":index=20;break;case "file_delete":index=21;break;
            case "control_eof":index=22;break;case "control_read":index=23;break;
            case "control_header":index=24;break;case "control_decode":index=25;break;
            default:return Lifetime.FailureTimerExit;
        }
        return code>65535?Lifetime.FailureTimerExit:0x01000000u|(index<<16)|code;
    }
    internal static NativeFailure LastError(string stage){return new NativeFailure(stage,unchecked((uint)Marshal.GetLastWin32Error()));}
    internal static Dictionary<string,object> Metadata(Exception error,string stage){
        var native=error as NativeFailure;
        return new Dictionary<string,object>{{"stage",native==null?stage:native.Stage},{"nativeError",native==null?0u:native.Code}};
    }
}
internal sealed class SecurityPrivilege : IDisposable {
    [StructLayout(LayoutKind.Sequential)] struct Luid { public uint Low;public int High; }
    [StructLayout(LayoutKind.Sequential)] struct Privileges {public uint Count;public Luid ID;public uint Attributes;}
    [DllImport("kernel32.dll")] static extern IntPtr GetCurrentProcess();
    [DllImport("advapi32.dll",SetLastError=true)] static extern bool OpenProcessToken(IntPtr process,uint access,out SafeFileHandle token);
    [DllImport("advapi32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool LookupPrivilegeValue(string system,string name,out Luid id);
    [DllImport("advapi32.dll",SetLastError=true)] static extern bool AdjustTokenPrivileges(SafeFileHandle token,bool disable,ref Privileges state,uint length,out Privileges previous,out uint needed);
    SafeFileHandle token;Privileges previous;
    internal SecurityPrivilege() {
        if(!OpenProcessToken(GetCurrentProcess(),0x28,out token))throw Failure.LastError("security_token");
        try {
            Luid id;if(!LookupPrivilegeValue(null,"SeSecurityPrivilege",out id))throw Failure.LastError("security_lookup");
            var state=new Privileges{Count=1,ID=id,Attributes=2};uint needed;
            if(!AdjustTokenPrivileges(token,false,ref state,(uint)Marshal.SizeOf(typeof(Privileges)),out previous,out needed)||Marshal.GetLastWin32Error()!=0)throw Failure.LastError("security_enable");
        } catch{token.Dispose();throw;}
    }
    public void Dispose(){
        try {Privileges ignored;uint needed;
            if(!AdjustTokenPrivileges(token,false,ref previous,(uint)Marshal.SizeOf(typeof(Privileges)),out ignored,out needed)||Marshal.GetLastWin32Error()!=0)throw Failure.LastError("security_restore");
        } finally {token.Dispose();}
    }
}
}
