import { useState } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IdentityForm } from "./IdentityForm";

describe("IdentityForm keyboard and mutation lifecycle",()=>{
  it("contains focus, restores it on Escape and blocks cancellation during a save",async()=>{
    let resolveSave!:()=>void;
    const save=vi.fn(()=>new Promise<void>(resolve=>{resolveSave=resolve;}));
    function Fixture(){const[open,setOpen]=useState(false);return <><button onClick={()=>setOpen(true)}>Open user form</button>{open&&<IdentityForm kind="users" initial={{id:"one",name:"Alice",status:"active",roles:["user"]}} onSave={save} onClose={()=>setOpen(false)}/>}</>;}
    render(<Fixture/>);
    const trigger=screen.getByRole("button",{name:"Open user form"});
    await userEvent.click(trigger);
    let dialog=await screen.findByRole("dialog",{name:"Edit user"});
    await waitFor(()=>expect(dialog).toContainElement(document.activeElement as HTMLElement));
    await userEvent.tab({shift:true});
    await waitFor(()=>expect(dialog).toContainElement(document.activeElement as HTMLElement));
    await userEvent.keyboard("{Escape}");
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    await waitFor(()=>expect(trigger).toHaveFocus());
    await userEvent.click(trigger);
    dialog=await screen.findByRole("dialog",{name:"Edit user"});
    await userEvent.click(within(dialog).getByRole("button",{name:"Save changes"}));
    await waitFor(()=>expect(save).toHaveBeenCalledTimes(1));
    expect(within(dialog).getByRole("button",{name:"Cancel"})).toBeDisabled();
    expect(within(dialog).getByLabelText("Name")).toBeDisabled();
    await userEvent.keyboard("{Escape}");
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    resolveSave();
    await waitFor(()=>expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("confirms disabling an active identity and retains existing external identity and custom approvals",async()=>{
    const confirm=vi.spyOn(window,"confirm").mockReturnValue(false);
    const save=vi.fn().mockResolvedValue(undefined);
    render(<IdentityForm kind="users" initial={{id:"one",status:"active",external_id:"external-one",roles:["user","existing-custom-role"]}} onSave={save} onClose={()=>{}}/>);
    await userEvent.selectOptions(screen.getByLabelText("Status"),"disabled");
    await userEvent.click(screen.getByRole("button",{name:"Save changes"}));
    expect(save).not.toHaveBeenCalled();
    confirm.mockReturnValue(true);
    await userEvent.click(screen.getByRole("button",{name:"Save changes"}));
    await waitFor(()=>expect(save).toHaveBeenCalledWith(expect.objectContaining({external_id:"external-one",roles:["user","existing-custom-role"],status:"disabled"})));
  });
});
