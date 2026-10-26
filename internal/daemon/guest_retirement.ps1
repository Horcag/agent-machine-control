$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = New-Object Text.UTF8Encoding($false, $true)
[Console]::OutputEncoding = New-Object Text.UTF8Encoding($false, $true)
Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Security.Principal;
using System.Text;
using Microsoft.Win32.SafeHandles;
public static class AMCOriginalProcessProbe {
    public sealed class Observation {
        public string status;
        public int pid;
        public long birthTicks;
        public string image = "";
        public string sid = "";
    }
    [StructLayout(LayoutKind.Sequential)] struct FT {public uint low,high;}
    [DllImport("kernel32.dll",SetLastError=true)] static extern SafeFileHandle OpenProcess(uint access,bool inherit,int pid);
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetProcessTimes(SafeFileHandle process,out FT creation,out FT exit,out FT kernel,out FT user);
    [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool QueryFullProcessImageName(SafeFileHandle process,uint flags,StringBuilder image,ref uint size);
    [DllImport("advapi32.dll",SetLastError=true)] static extern bool OpenProcessToken(SafeFileHandle process,uint access,out SafeFileHandle token);
    [DllImport("kernel32.dll",SetLastError=true)] static extern uint WaitForSingleObject(SafeFileHandle process,uint timeout);
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool K32EnumProcesses([Out] uint[] processes,uint size,out uint needed);
    static bool Missing(int pid) {
        var processes = new uint[65536];uint needed;
        if(!K32EnumProcesses(processes,(uint)(processes.Length*4),out needed))throw new Win32Exception();
        return InventoryMissing(pid,processes,needed);
    }
    public static bool InventoryMissing(int pid,uint[] processes,uint needed) {
        if(needed==0||needed>=processes.Length*4||needed%4!=0)throw new Win32Exception();
        for(int i=0;i<needed/4;i++)if(processes[i]==pid)return false;
        return true;
    }
    public static Observation Read(int pid,long originalBirth) {
        if(pid<=0||originalBirth<=0)throw new ArgumentException();
        using(var process=OpenProcess(0x101000,false,pid)) {
            if(process.IsInvalid) {
                int error=Marshal.GetLastWin32Error();
                if(error==87&&Missing(pid))return new Observation{status="missing",pid=pid};
                throw new Win32Exception(error);
            }
            FT creation,exit,kernel,user;
            if(!GetProcessTimes(process,out creation,out exit,out kernel,out user))throw new Win32Exception();
            long birth=DateTime.FromFileTimeUtc(((long)creation.high<<32)|creation.low).Ticks;
            var result=new Observation{pid=pid,birthTicks=birth};
            if(birth!=originalBirth){result.status="reused";return result;}
            var image=new StringBuilder(32768);uint size=(uint)image.Capacity;
            if(!QueryFullProcessImageName(process,0,image,ref size))throw new Win32Exception();
            result.image=image.ToString();
            SafeFileHandle token;
            if(!OpenProcessToken(process,8,out token))throw new Win32Exception();
            using(token)using(var identity=new WindowsIdentity(token.DangerousGetHandle()))result.sid=identity.User.Value;
            uint state=WaitForSingleObject(process,0);
            if(state!=0&&state!=258)throw new Win32Exception();
            result.status=state==0?"exited":"live";
            return result;
        }
    }
}
'@
$observed = [AMCOriginalProcessProbe]::Read([int]$original.pid, [long]$original.birthTicks)
@{ original = $original; status = $observed.status; observed = @{ pid = $observed.pid; birthTicks = $observed.birthTicks; image = $observed.image; sid = $observed.sid } } | ConvertTo-Json -Compress -Depth 4
