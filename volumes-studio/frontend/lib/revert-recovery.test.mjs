import test from "node:test";
import assert from "node:assert/strict";
import {readRevertCommand} from "./revert-recovery.ts";
test("revert recovery binds scope and retains exact request bytes",()=>{
 const body={request_id:"original",name:"Inverse",commit:"a".repeat(64),parent:"b".repeat(64)};
 const command={library:"volume",body:JSON.stringify(body,null,2)};
 assert.deepEqual(readRevertCommand(JSON.stringify(command),"volume"),command);
 assert.throws(()=>readRevertCommand(JSON.stringify(command),"foreign"));
 for(const change of [{request_id:""},{name:"main"},{name:" name "},{commit:"bad"},{parent:null},{parent:""}])assert.throws(()=>readRevertCommand(JSON.stringify({...command,body:JSON.stringify({...body,...change})}),"volume"));
 for(const raw of ["null","{",'"value"'," ".repeat(8193)])assert.throws(()=>readRevertCommand(raw,"volume"));
});
