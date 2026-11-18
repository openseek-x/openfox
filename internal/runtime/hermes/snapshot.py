"""OpenFox-owned bounded workspace snapshots for the fixed rg handler."""
import os, pathlib, stat, time, shutil

IGNORE_NAMES = ('.gitignore', '.ignore', '.rgignore')
SECRET_NAMES = {'.ssh', '.aws', '.hermes', '__pycache__'}

class SnapshotError(Exception): pass

class Snapshot:
    def __init__(self, source_fd, dest, *, timeout=60, max_bytes=64<<20, max_file=8<<20,
                 max_files=10000, max_entries=50000, cancel=lambda:False, blocked=lambda path:False):
        self.source_fd, self.dest = source_fd, pathlib.Path(dest)
        self.deadline = time.monotonic()+timeout
        self.max_bytes, self.max_file = max_bytes, max_file
        self.max_files, self.max_entries = max_files, max_entries
        self.cancel, self.blocked = cancel, blocked
        self.git_metadata_warning = False
        self.bytes = self.files = self.entries = self.pruned = self.links = 0

    def check(self):
        if self.cancel(): raise SnapshotError('cancelled')
        if time.monotonic() >= self.deadline: raise SnapshotError('snapshot/search deadline exceeded')

    def open_regular(self, parent, name):
        fd=os.open(name,os.O_RDONLY|os.O_NONBLOCK|os.O_NOFOLLOW,dir_fd=parent)
        if not stat.S_ISREG(os.fstat(fd).st_mode):
            os.close(fd);raise SnapshotError('non-regular file changed during snapshot')
        return fd

    def read_metadata(self, parent, name):
        try: fd=self.open_regular(parent,name)
        except FileNotFoundError: return None
        try:
            before=os.fstat(fd)
            if before.st_size>65536: raise SnapshotError('ignore metadata exceeds 64 KiB')
            chunks=[]
            length=0
            while True:
                self.check()
                chunk=os.read(fd, min(65537-length, 65536))
                if not chunk:break
                chunks.append(chunk);length+=len(chunk)
                if length>65536:raise SnapshotError('ignore metadata grew')
            raw=b''.join(chunks)
            if self.version(before)!=self.version(os.fstat(fd)): raise SnapshotError('ignore file changed')
            if len(raw)>65536: raise SnapshotError('ignore metadata grew')
            return raw
        finally:os.close(fd)

    @staticmethod
    def version(info): return (info.st_dev,info.st_ino,info.st_size,info.st_mtime_ns,info.st_ctime_ns)

    @staticmethod
    def simple_match(rule, relative, directory):
        # A conservative optimization only. Unknown positive globs retain files
        # for the fixed rg matcher. Unknown negations disable pruning for safety.
        rule=rule.rstrip('\n\r')
        if not rule or rule.startswith('#'):return None
        negative=rule.startswith('!')
        if negative:rule=rule[1:]
        directory_only=rule.endswith('/')
        rule=rule.rstrip('/')
        anchored=rule.startswith('/')
        rule=rule.lstrip('/')
        if rule.startswith('**/') and '/' not in rule[3:]:rule=rule[3:];anchored=False
        if not rule or any(c in rule for c in '*?[\\ ') or rule.endswith(' '):
            return 'uncertain' if negative else None
        if directory_only and not directory:return None
        hit=(relative==rule) if anchored or '/' in rule else relative.rsplit('/',1)[-1]==rule
        return (not negative) if hit else None

    def ignored(self, rel, directory, scopes, glob):
        if glob:return False # -g can override ignores. Let fixed rg decide after bounded copying.
        decision=None
        for kind in IGNORE_NAMES:
            for base, rules in scopes[kind]:
                if base and not rel.startswith(base+'/'):continue
                relative=rel[len(base)+1:] if base else rel
                for rule in rules:
                    match=self.simple_match(rule,relative,directory)
                    if match=='uncertain':return False
                    if match is not None:decision=match
        return decision is True

    def may_unhide(self, rel, directory, scopes):
        # A negation can whitelist hidden entries. Unknown negations retain
        # candidates for rg; unrelated literal negations need not force a
        # copy of an otherwise hidden dependency cache such as .pnpm.
        for kind in IGNORE_NAMES:
            for base, rules in scopes[kind]:
                if base and not rel.startswith(base+'/'):continue
                relative=rel[len(base)+1:] if base else rel
                for rule in rules:
                    if rule.startswith('!'):
                        result=self.simple_match(rule,relative,directory)
                        if result is False or result=='uncertain':return True
        return False

    def copy_file(self,parent,name,target):
        fd=self.open_regular(parent,name)
        try:
            before=os.fstat(fd)
            self.files+=1
            if self.files>self.max_files:raise SnapshotError('file count exceeded')
            if before.st_size>self.max_file:raise SnapshotError('individual file exceeds 8 MiB')
            if self.bytes+before.st_size>self.max_bytes:raise SnapshotError('snapshot exceeds 64 MiB')
            target.parent.mkdir(parents=True,exist_ok=True)
            with target.open('xb') as out:
                copied=0
                while True:
                    self.check()
                    data=os.read(fd,65536)
                    if not data:break
                    copied+=len(data);self.bytes+=len(data)
                    if copied>self.max_file or self.bytes>self.max_bytes:raise SnapshotError('copy grew beyond budget')
                    out.write(data)
            if self.version(before)!=self.version(os.fstat(fd)):raise SnapshotError('source changed during copy')
        finally:os.close(fd)

    def metadata(self,fd,rel,scopes,git):
        dst=self.dest/rel
        dst.mkdir(parents=True,exist_ok=True)
        current={name:list(scopes[name]) for name in IGNORE_NAMES}
        try:os.stat('.git',dir_fd=fd,follow_symlinks=False);has_git=True
        except FileNotFoundError:has_git=False
        if has_git:
            git=True
            current['.gitignore']=[]
            (dst/'.git').mkdir(exist_ok=True)
            try:git_fd=os.open('.git',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
            except NotADirectoryError:
                git_fd=None # No authority to follow a shared worktree gitdir.
                self.git_metadata_warning=True
            if git_fd is not None:
                try:
                    try:info_fd=os.open('info',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=git_fd)
                    except FileNotFoundError:info_fd=None
                    if info_fd is not None:
                        try:exclude=self.read_metadata(info_fd,'exclude')
                        finally:os.close(info_fd)
                        if exclude is not None:
                            self.bytes+=len(exclude)
                            if self.bytes>self.max_bytes:raise SnapshotError('metadata exceeds total budget')
                            (dst/'.git/info').mkdir(exist_ok=True)
                            (dst/'.git/info/exclude').write_bytes(exclude)
                            current['.gitignore'].insert(0,(rel,exclude.decode('utf-8',errors='replace').splitlines()))
                finally:os.close(git_fd)
        for name in IGNORE_NAMES:
            data=self.read_metadata(fd,name)
            if data is not None:
                self.bytes+=len(data)
                if self.bytes>self.max_bytes:raise SnapshotError('metadata exceeds total budget')
                (dst/name).write_bytes(data)
                if name!='.gitignore' or git:
                    current[name].append((rel,data.decode('utf-8',errors='replace').splitlines()))
        return current,git

    def walk(self,fd,rel,scopes,git,glob,depth=0):
        self.check()
        if depth>64:raise SnapshotError('directory depth exceeded')
        names=[]
        with os.scandir(fd) as scan:
            for entry in scan:
                self.check()
                self.entries+=1
                if self.entries>self.max_entries:raise SnapshotError('enumeration budget exceeded')
                names.append(entry.name)
        current,git=self.metadata(fd,rel,scopes,git)
        for name in names:
            self.check()
            if name.casefold() == '.git' or name in IGNORE_NAMES or name in SECRET_NAMES or name.startswith('.env'):continue
            if self.blocked((rel+'/'+name).lstrip('/')):continue
            info=os.stat(name,dir_fd=fd,follow_symlinks=False)
            if stat.S_ISLNK(info.st_mode):self.links+=1;continue
            child=(rel+'/'+name).lstrip('/')
            directory=stat.S_ISDIR(info.st_mode)
            if name.startswith('.') and not glob and not self.may_unhide(child,directory,current):continue
            if self.ignored(child,directory,current,glob):self.pruned+=1;continue
            if directory:
                next_fd=os.open(name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
                try:self.walk(next_fd,child,current,git,glob,depth+1)
                finally:os.close(next_fd)
            elif stat.S_ISREG(info.st_mode):self.copy_file(fd,name,self.dest/child)
            else:raise SnapshotError('unsupported file type')

    def run(self,glob=None,relative_root='',root_is_file=False):
        fd=os.dup(self.source_fd)
        try:
            scopes={name:[] for name in IGNORE_NAMES};git=False;rel=''
            parts=pathlib.PurePosixPath(relative_root).parts
            if any(part.casefold()=='.git' for part in parts):
                raise SnapshotError('Git control files are not searchable content')
            if any(p in ('..','.') for p in parts) or relative_root.startswith('/') or len(parts)>64:
                raise SnapshotError('invalid selected root')
            directory_parts=parts[:-1] if root_is_file else parts
            for part in directory_parts:
                self.check()
                scopes,git=self.metadata(fd,rel,scopes,git)
                next_fd=os.open(part,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
                os.close(fd);fd=next_fd;rel=(rel+'/'+part).lstrip('/')
            if root_is_file:
                if not parts or parts[-1]=='.git':raise SnapshotError('Git control files are not searchable content')
                self.metadata(fd,rel,scopes,git)
                target=self.dest/relative_root
                if not target.exists():self.copy_file(fd,parts[-1],target)
            else:self.walk(fd,rel,scopes,git,glob,len(parts))
            return {'bytes':self.bytes,'files':self.files,'entries':self.entries,'pruned':self.pruned,'symlinks_skipped':self.links}
        except BaseException:
            shutil.rmtree(self.dest,ignore_errors=True)
            raise
        finally:os.close(fd)
