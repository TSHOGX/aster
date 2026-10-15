import { WorkflowEditor } from "../../../../../components/workflow-editor";
export default async function Page({ params }: { params: Promise<{ group:string; workflow:string }> }) { const value=await params; return <WorkflowEditor group={value.group} workflow={value.workflow}/>; }
