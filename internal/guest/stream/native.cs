namespace AMCStream {
using System;
using System.Collections.Generic;
using System.IO;
using System.Runtime.InteropServices;
using System.Security.AccessControl;
using System.Security.Cryptography;
using Microsoft.Win32.SafeHandles;


internal static class Native {
    [StructLayout(LayoutKind.Sequential)] internal struct Info {
        public uint Attributes; public System.Runtime.InteropServices.ComTypes.FILETIME Creation,Access,Write;
        public uint Volume,SizeHigh,SizeLow,Links,IndexHigh,IndexLow;
    }
    [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern SafeFileHandle CreateFile(string path,uint access,uint share,IntPtr security,uint creation,uint flags,IntPtr template);
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetFileInformationByHandle(SafeFileHandle handle,out Info info);
    [StructLayout(LayoutKind.Sequential)] struct FileIDInfo { public ulong Volume;[MarshalAs(UnmanagedType.ByValArray,SizeConst=16)]public byte[] ID; }
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool GetFileInformationByHandleEx(SafeFileHandle handle,int type,out FileIDInfo info,uint size);
    static FileIDInfo FileID(SafeFileHandle handle){FileIDInfo info;if(!GetFileInformationByHandleEx(handle,18,out info,24))throw Failure.LastError("file_id_read");return info;}
    [DllImport("kernel32.dll",SetLastError=true)] static extern bool SetFileInformationByHandle(SafeFileHandle handle,int type,byte[] data,uint size);
    [DllImport("kernel32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern uint GetFileAttributes(string path);
    [DllImport("advapi32.dll",SetLastError=true)] static extern bool GetKernelObjectSecurity(SafeFileHandle handle,uint requested,byte[] descriptor,uint length,out uint needed);
    internal static string FullPath(string path) {
        if(String.IsNullOrEmpty(path)||path.IndexOfAny(new char[]{'*','?','\0'})>=0||!Path.IsPathRooted(path)||path.StartsWith(@"\\")||path.IndexOf(':',2)>=0)throw new InvalidDataException();
        string full=Path.GetFullPath(path);if(full.Length>240||full.Length<4)throw new InvalidDataException();return full;
    }
    static SafeFileHandle Handle(string path,uint access,uint share,uint creation,uint flags=0,string stage="file_open") {
        SafeFileHandle handle=null;try{using(var privilege=new SecurityPrivilege()){handle=CreateFile(path,access,share,IntPtr.Zero,creation,0x02200000|flags,IntPtr.Zero);if(handle.IsInvalid)throw Failure.LastError(stage);}}catch{if(handle!=null)handle.Dispose();throw;} // OPEN_REPARSE_POINT | BACKUP_SEMANTICS
        if(handle.IsInvalid){handle.Dispose();throw new IOException("file_unavailable");}
        try {Info info=Metadata(handle);if((info.Attributes&0x400)!=0)throw new IOException("reparse_point");return handle;}catch{handle.Dispose();throw;}
    }
    static Info Metadata(SafeFileHandle handle){Info info;if(!GetFileInformationByHandle(handle,out info))throw Failure.LastError("metadata_read");return info;}
    static long Birth(Info info){return ((long)info.Creation.dwHighDateTime<<32)|(uint)info.Creation.dwLowDateTime;}

    static RawSecurityDescriptor Security(SafeFileHandle handle) {
        // Require all SDDL sections, including SACL. Missing privilege is a
        // preserved/inaccessible result, never a weakened identity fallback.
        uint needed;GetKernelObjectSecurity(handle,15,null,0,out needed);
        if(needed==0)throw Failure.LastError("security_size");
        if(needed>65536)throw new NativeFailure("security_size",0);
        byte[] bytes=new byte[needed];if(!GetKernelObjectSecurity(handle,15,bytes,needed,out needed))throw Failure.LastError("security_read");
        try{return new RawSecurityDescriptor(bytes,0);}catch{throw new NativeFailure("security_parse",0);}
    }
    static string SecurityHash(RawSecurityDescriptor descriptor) {
        using(var sha=SHA256.Create())return Worker.Hex(sha.ComputeHash(System.Text.Encoding.UTF8.GetBytes(descriptor.GetSddlForm(AccessControlSections.All))));
    }
    internal sealed class Parents : IDisposable {
        internal readonly List<SafeFileHandle> Handles=new List<SafeFileHandle>();internal string Path;
        internal Dictionary<string,object> Identity(){var handle=Handles[Handles.Count-1];var info=Metadata(handle);var id=FileID(handle);var security=Security(handle);
            return new Dictionary<string,object>{{"path",Path},{"volume",id.Volume},{"fileId",Worker.Hex(id.ID)},{"creationTime",Birth(info)},{"owner",security.Owner.Value},{"sddlHash",SecurityHash(security)}};
        }
        public void Dispose(){foreach(var h in Handles)h.Dispose();}
    }
    internal static Parents LockParents(string path) {
        var parents=new Parents();try {
            string root=Path.GetPathRoot(path);string parent=System.IO.Path.GetDirectoryName(path);
            parents.Path=parent;string current=root;
            parents.Handles.Add(Handle(current,0x01020080,1,3,0,"ancestor_open")); // full security read; sharing read only excludes rename/write
            string remainder=parent.Substring(root.Length);
            foreach(string component in remainder.Split(new char[]{'\\'},StringSplitOptions.RemoveEmptyEntries)) {
                current=System.IO.Path.Combine(current,component);parents.Handles.Add(Handle(current,0x01020080,1,3,0,current==parent?"workspace_open":"ancestor_open"));
            }
            return parents;
        }catch{parents.Dispose();throw;}
    }
    internal sealed class File : IDisposable {
        internal FileStream Stream;internal SafeFileHandle Handle;
        internal File(SafeFileHandle handle,bool writable){Handle=handle;try{Stream=new FileStream(handle,writable?FileAccess.ReadWrite:FileAccess.Read,65536,false);}catch{handle.Dispose();throw;}}
        internal Dictionary<string,object> Identity(string path,Parents parents,string knownHash=null) {
            var info=Metadata(Handle);var id=FileID(Handle);if(info.Links!=1||(info.Attributes&0x10)!=0)throw new IOException("nonexclusive_file");
            var security=Security(Handle);long position=Stream.Position;Stream.Position=0;string hash;
            try{if(knownHash!=null)hash=knownHash;else using(var sha=SHA256.Create())hash=Worker.Hex(sha.ComputeHash(Stream));}finally{Stream.Position=position;}
            var rootHandle=parents.Handles[parents.Handles.Count-1];var root=Metadata(rootHandle);var rootID=FileID(rootHandle);var rootSecurity=Security(parents.Handles[parents.Handles.Count-1]);
            return new Dictionary<string,object>{{"path",path},{"volume",id.Volume},{"fileId",Worker.Hex(id.ID)},{"creationTime",Birth(info)},
                {"owner",security.Owner.Value},{"sddlHash",SecurityHash(security)},{"size",Stream.Length},{"sha256",hash},
                {"rootPath",parents.Path},{"rootVolume",rootID.Volume},{"rootFileId",Worker.Hex(rootID.ID)},{"rootCreationTime",Birth(root)},
                {"rootOwner",rootSecurity.Owner.Value},{"rootSDDLHash",SecurityHash(rootSecurity)}};
        }
        internal void Delete(){Disposition(true);}
        // ON_CLOSE | DO_NOT_DELETE clears atomic FILE_FLAG_DELETE_ON_CLOSE.
        // Legacy FileDispositionInfo cannot clear a create-time delete-on-close flag.
        internal void Commit(){byte[] flags=BitConverter.GetBytes(8u);if(!SetFileInformationByHandle(Handle,21,flags,4))throw Failure.LastError("file_commit");}
        void Disposition(bool delete){byte[] disposition=new byte[]{delete?(byte)1:(byte)0};if(!SetFileInformationByHandle(Handle,4,disposition,1))throw Failure.LastError("file_delete");}
        public void Dispose(){Stream.Dispose();}
    }
    internal static File Create(string path){
        // The kernel owns cleanup from the instant CREATE_NEW succeeds, including
        // metadata validation, FileStream construction failure and abrupt EOF kill.
        // Unsupported extended commit never falls back to a gap-prone legacy path.
        return new File(Handle(path,0xC1030000,0,1,0x04000000),true);
    }
    internal static File Open(string path){return new File(Handle(path,0x81030000,0,3),false);}
    internal static bool Same(Dictionary<string,object> expected,Dictionary<string,object> actual) {
        if(expected.Count!=actual.Count)return false;
        foreach(var pair in actual){object value;if(!expected.TryGetValue(pair.Key,out value)||Convert.ToString(value,System.Globalization.CultureInfo.InvariantCulture)!=Convert.ToString(pair.Value,System.Globalization.CultureInfo.InvariantCulture))return false;}
        return true;
    }
    internal static bool Absent(string path){if(GetFileAttributes(path)!=UInt32.MaxValue)return false;int error=Marshal.GetLastWin32Error();return error==2||error==3;}
}
}
