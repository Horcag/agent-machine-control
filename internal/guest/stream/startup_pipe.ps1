# The inherited pipe may require overlapped I/O before C# can be compiled.
# Define only these fixed native declarations; no helper compiler is started.
$ErrorActionPreference='Stop';
$assembly=[AppDomain]::CurrentDomain.DefineDynamicAssembly([Reflection.AssemblyName]::new('AMCBootstrapPipe'),[Reflection.Emit.AssemblyBuilderAccess]::Run);
$type=$assembly.DefineDynamicModule('n').DefineType('AMCBootstrapPipe',[Reflection.TypeAttributes]::Public);
$spec=@(@('GetStdHandle','kernel32.dll',[IntPtr],[type[]]@([int])),@('GetFileType','kernel32.dll',[uint32],[type[]]@([IntPtr])),@('NtQueryInformationFile','ntdll.dll',[int],[type[]]@([IntPtr],[IntPtr],[IntPtr],[uint32],[int])));
foreach($s in $spec){
    $method=$type.DefinePInvokeMethod($s[0],$s[1],([Reflection.MethodAttributes]::Public-bor[Reflection.MethodAttributes]::Static-bor[Reflection.MethodAttributes]::PinvokeImpl),[Reflection.CallingConventions]::Standard,$s[2],$s[3],[Runtime.InteropServices.CallingConvention]::Winapi,[Runtime.InteropServices.CharSet]::Unicode);
    $method.SetImplementationFlags($method.GetMethodImplementationFlags()-bor[Reflection.MethodImplAttributes]::PreserveSig);
};
$Native=$type.CreateType();

function Get-AMCAsyncMode([int]$status,[int]$ioStatus,[long]$information,[int]$mode){
    if($status-eq0x103){throw [IO.IOException]::new('214')};
    if($status-ne0-or$ioStatus-ne0){throw [IO.IOException]::new('211')};
    if($information-ne4){throw [IO.IOException]::new('212')};
    $synchronous=$mode-band0x30;
    if($synchronous-eq0x30){throw [IO.IOException]::new('211')};
    return ($synchronous-eq0);
};

function Open-AMCStandardStream([int]$id,[IO.FileAccess]$access){
    $handle=$Native::GetStdHandle($id);
    if($handle-eq[IntPtr]::Zero-or$handle-eq[IntPtr]::new(-1)-or$Native::GetFileType($handle)-ne3){throw [IO.IOException]::new('211')};
    $iosb=[Runtime.InteropServices.Marshal]::AllocHGlobal(2*[IntPtr]::Size);
    $modeBuffer=[IntPtr]::Zero;$status=-1;
    try{
        $modeBuffer=[Runtime.InteropServices.Marshal]::AllocHGlobal(4);
        for($offset=0;$offset-lt2*[IntPtr]::Size;$offset++){[Runtime.InteropServices.Marshal]::WriteByte($iosb,$offset,0)};
        [Runtime.InteropServices.Marshal]::WriteInt32($modeBuffer,0);
        $status=$Native::NtQueryInformationFile($handle,$iosb,$modeBuffer,4,16);
        if($status-eq0x103){throw [IO.IOException]::new('214')};
        $ioStatus=[Runtime.InteropServices.Marshal]::ReadInt32($iosb);
        $information=[Runtime.InteropServices.Marshal]::ReadIntPtr($iosb,[IntPtr]::Size).ToInt64();
        $mode=[Runtime.InteropServices.Marshal]::ReadInt32($modeBuffer);
        $async=Get-AMCAsyncMode $status $ioStatus $information $mode;
    }finally{
        # Pending native I/O can still own these buffers; process exit reclaims them.
        if($status-ne0x103){
            if($modeBuffer-ne[IntPtr]::Zero){[Runtime.InteropServices.Marshal]::FreeHGlobal($modeBuffer)};
            [Runtime.InteropServices.Marshal]::FreeHGlobal($iosb);
        };
    };
    $borrowed=[Microsoft.Win32.SafeHandles.SafeFileHandle]::new($handle,$false);
    try{return [IO.FileStream]::new($borrowed,$access,65536,$async)}catch{$borrowed.Dispose();throw};
};

function Read-AMCSource([IO.Stream]$i){
    [Console]::Error.WriteLine('AMC_STARTUP_V1:1:before_header_read');
    $h=New-Object byte[] 4;$n=0;
    while($n-lt4){$r=$i.Read($h,$n,4-$n);if($r-eq0){exit 1};$n+=$r};
    [Array]::Reverse($h);$l=[BitConverter]::ToUInt32($h,0);
    if($l-gt262144){exit 1};
    [Console]::Error.WriteLine('AMC_STARTUP_V1:2:before_source_read');
    $b=New-Object byte[] $l;$n=0;
    while($n-lt$l){$r=$i.Read($b,$n,$l-$n);if($r-eq0){exit 1};$n+=$r};
    [Console]::Error.WriteLine('AMC_STARTUP_V1:3:before_compile');
    return ,$b;
};
# Bind each inherited handle once. The source reader and worker share the same
# stream; reopening an overlapped handle can fail its completion-port binding.
$i=Open-AMCStandardStream -10 ([IO.FileAccess]::Read);
try{
    $b=Read-AMCSource $i;
    Add-Type -TypeDefinition ([Text.Encoding]::UTF8.GetString($b)) -ReferencedAssemblies @('System.dll','System.Core.dll','System.Web.Extensions.dll',[Management.Automation.PowerShell].Assembly.Location);
    $o=Open-AMCStandardStream -11 ([IO.FileAccess]::Write);
    try{
        [Console]::Error.WriteLine('AMC_STARTUP_V1:4:before_worker_run');
        [AMCStream.Worker]::Run($i,$o);
    }finally{$o.Dispose()};
}finally{$i.Dispose()};
