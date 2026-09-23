import { expect, it } from "vitest";
import { APIError, type FilePage } from "./project-api";
import { WorkspaceFileSearch } from "./workspace-file-search";
const file = (name: string) => ({name, kind:"file", size:1, modified:0});
const dir = (name: string) => ({...file(name), kind:"directory"});
const signal = () => new AbortController().signal;

it("finds case-insensitive extension matches in unopened nested folders and later pages", async () => {
  const pages: Record<string, FilePage> = {
    ":":{entries:[dir("reports"),file("root.html")],next:"second"},
    ":second":{entries:[file("later.XLSX")]},
    "reports:":{entries:[dir("nested"),file("data.xlsx")]},
    "reports/nested:":{entries:[file("deep.XLSX"),file("chart.html"),{...dir("link"),kind:"symlink"}]},
  };
  for (const [query,expected] of [[" XLSX ",["reports/nested/deep.XLSX","reports/data.xlsx","later.XLSX"]],["html",["reports/nested/chart.html","root.html"]]] as const) {
    const search = new WorkspaceFileSearch(query, async (path, after) => {
      const page=pages[`${path}:${after}`]; expect(page).toBeDefined(); return page;
    });
    const result=await search.next(signal());
    expect(result.matches.map(m=>m.path)).toEqual(expected);
    expect(result.done).toBe(true); expect(result.limited).toBe(false);
  }
});

it("replaces a bounded result window and continues without dropping matches", async () => {
  const search=new WorkspaceFileSearch("xlsx",async (_,after)=>{
    const start=Number(after || 0);
    return {entries:Array.from({length:Math.min(100,250-start)},(_,i)=>file(`file-${start+i}.xlsx`)),next:start+100<250?String(start+100):undefined};
  });
  const paths:string[]=[];
  for(let n=0;n<3;n++){
    const result=await search.next(signal());
    expect(result.matches.length).toBeLessThanOrEqual(100);
    paths.push(...result.matches.map(m=>m.path));
  }
  expect(paths).toHaveLength(250);expect(new Set(paths).size).toBe(250);
  expect((await search.next(signal())).done).toBe(true);
});

it("reports incomplete traversal honestly and resumes its directory demand",async()=>{
  const search=new WorkspaceFileSearch("html",async path=>path?{entries:[file("page.html")]}:{entries:[dir("one"),dir("two")]});
  const first=await search.next(signal(),1);
  expect(first.matches).toEqual([]);expect(first.done).toBe(false);
  const next=await search.next(signal());
  expect(next.matches.map(m=>m.path)).toEqual(["one/page.html","two/page.html"]);
  expect(next.done).toBe(true);
});

it("expired cursors skip processed names without losing later matches",async()=>{
  let expired=false;
  const search=new WorkspaceFileSearch("xlsx",async(_,after)=>{
    if(after==="old"){expired=true;throw new APIError(409,"snapshot expired");}
    if(after==="new")return {entries:[file("z.xlsx")]};
    return {entries:[file("a.xlsx"),file("b.txt")],next:expired?"new":"old"};
  });
  const first=await search.next(signal(),1);
  expect(first.matches.map(m=>m.path)).toEqual(["a.xlsx"]);
  const rest=await search.next(signal());
  expect(rest.matches.map(m=>m.path)).toEqual(["z.xlsx"]);expect(rest.done).toBe(true);
});

it("cancels superseded searches and keeps partial matches across a retryable read failure",async()=>{
  const abort=new AbortController();let attempts=0;
  const search=new WorkspaceFileSearch("html",async(path)=>{
    if(!path)return {entries:[file("a.html"),dir("child")]};
    if(++attempts===1)throw new APIError(503,"busy");
    return {entries:[file("b.html")]};
  });
  await expect(search.next(abort.signal)).rejects.toMatchObject({status:503});
  expect((await search.next(abort.signal)).matches.map(m=>m.path)).toEqual(["a.html","child/b.html"]);
  abort.abort();await expect(search.next(abort.signal)).rejects.toMatchObject({name:"AbortError"});
});
