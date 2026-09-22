export type FileInvalidation = {path?:string;old_path?:string;operation?:string;entry_type?:string;reset?:boolean;reason?:string;epoch?:string;sequence?:string};
const parent = (p:string) => p.includes("/") ? p.slice(0,p.lastIndexOf("/")) : "";
const within = (path:string,root:string) => path===root || path.startsWith(root+"/");
export function fileImpact(event:FileInvalidation,selected:string,directories:readonly string[]) {
  const paths=[event.path,event.old_path].filter((p):p is string=>typeof p==="string" && !!p);
  const gap=!!event.reset || !paths.length;
  const tree=event.entry_type==="directory" || ["rename","remove"].includes(event.operation||"");
  return {
    gap,
    selected:!!selected && (gap || paths.some(p=>p===selected || (tree && within(selected,p)))),
    directories:gap ? [...new Set(["",...directories])].slice(0,16) : directories.filter(d=>paths.some(p=>parent(p)===d || (tree && within(d,p)))),
  };
}
